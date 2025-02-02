package enmime

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/textproto"
	"sort"
	"time"

	"github.com/jhillyerd/enmime/internal/coding"
	"github.com/jhillyerd/enmime/internal/stringutil"
)

// b64Percent determines the percent of non-ASCII characters enmime will tolerate before switching
// from quoted-printable to base64 encoding.
const b64Percent = 20

type TransferEncoding byte

const (
	Te7Bit TransferEncoding = iota
	TeQuoted
	TeBase64
	TeRaw
	TeAuto
)

const (
	base64EncodedLineLen = 76
	base64DecodedLineLen = base64EncodedLineLen * 3 / 4 // this is ok since lineLen is divisible by 4
	linesPerChunk        = 128
	readChunkSize        = base64DecodedLineLen * linesPerChunk
)

var crnl = []byte{'\r', '\n'}
var nlnl = []byte{'\n', '\n'}

// Encode writes this Part and all its children to the specified writer in MIME format
// using the specified content transfer encoding.
func (p *Part) EncodeCustom(writer io.Writer, textCte TransferEncoding, useLF bool) error {

	if p.Header == nil {
		p.Header = make(textproto.MIMEHeader)
	}
	if p.ContentReader != nil {
		// read some data in order to check whether the content is empty
		p.Content = make([]byte, readChunkSize)
		n, err := p.ContentReader.Read(p.Content)
		if err != nil && err != io.EOF {
			return err
		}
		p.Content = p.Content[:n]
	}
	cte := p.setupMIMEHeaders(textCte)

	// Encode this part.
	b := bufio.NewWriter(writer)
	if err := p.encodeHeader(b, useLF, useLF); err != nil {
		return err
	}

	if useLF {
		if _, err := b.Write(nlnl); err != nil {
			return err
		}
	}

	if len(p.Content) > 0 {
		if !useLF {
			if _, err := b.Write(crnl); err != nil {
				return err
			}
		}
		if err := p.encodeContent(b, cte, useLF); err != nil {
			return err
		}
	}

	if p.FirstChild == nil {
		return b.Flush()
	}
	// Encode children.
	var marker, endMarker []byte

	if useLF {
		marker = []byte("\n--" + p.Boundary)
		endMarker = []byte("\n--" + p.Boundary + "--")
	} else {
		endMarker = []byte("\r\n--" + p.Boundary + "--")
		marker = endMarker[:len(endMarker)-2]
	}
	c := p.FirstChild
	for c != nil {
		if _, err := b.Write(marker); err != nil {
			return err
		}
		if useLF {
			if _, err := b.Write([]byte{'\n'}); err != nil {
				return err
			}
		} else {
			if _, err := b.Write(crnl); err != nil {
				return err
			}
		}
		if err := c.EncodeCustom(b, textCte, useLF); err != nil {
			return err
		}
		c = c.NextSibling
	}
	if _, err := b.Write(endMarker); err != nil {
		return err
	}
	if useLF {
		if _, err := b.Write([]byte{'\n'}); err != nil {
			return err
		}
	} else {
		if _, err := b.Write(crnl); err != nil {
			return err
		}
	}
	return b.Flush()
}

// Encode writes this Part and all its children to the specified writer in MIME format
// and using the specified content transfer encoding.
func (p *Part) EncodeUsingCte(writer io.Writer, cte TransferEncoding) error {
	return p.EncodeCustom(writer, cte, true)
}

// Encode writes this Part and all its children to the specified writer in MIME format.
func (p *Part) Encode(writer io.Writer) error {
	return p.EncodeCustom(writer, TeAuto, true)
}

// setupMIMEHeaders determines content transfer encoding, generates a boundary string if required,
// then sets the Content-Type (type, charset, filename, boundary) and Content-Disposition headers.
func (p *Part) setupMIMEHeaders(textCteHint TransferEncoding) TransferEncoding {
	// Determine content transfer encoding.

	// If we are encoding a part that previously had content-transfer-encoding set, unset it so
	// the correct encoding detection can be done below.
	if p.parser != nil && !p.parser.rawContent {
		p.Header.Del(hnContentEncoding)
	}

	cte := Te7Bit
	if len(p.Content) > 0 {
		cte = TeBase64
		if p.TextContent() && p.ContentReader == nil {
			if textCteHint == TeAuto {
				cte = selectTransferEncoding(p.Content, false)
			} else {
				cte = textCteHint
			}

			if p.Charset == "" {
				p.Charset = utf8
			}
		}
		// RFC 2045: 7bit is assumed if CTE header not present.
		switch cte {
		case TeBase64:
			p.Header.Set(hnContentEncoding, cteBase64)
		case TeQuoted:
			p.Header.Set(hnContentEncoding, cteQuotedPrintable)
		}
	}
	// Setup headers.
	if p.FirstChild != nil && p.Boundary == "" {
		// Multipart, generate random boundary marker.
		p.Boundary = "enmime-" + stringutil.UUID(p.randSource)
	}
	if p.ContentID != "" {
		p.Header.Set(hnContentID, coding.ToIDHeader(p.ContentID))
	}
	fileName := p.FileName
	switch selectTransferEncoding([]byte(p.FileName), true) {
	case TeBase64:
		fileName = mime.BEncoding.Encode(utf8, p.FileName)
	case TeQuoted:
		fileName = mime.QEncoding.Encode(utf8, p.FileName)
	}
	if p.ContentType != "" {
		// Build content type header.
		param := make(map[string]string)
		for k, v := range p.ContentTypeParams {
			param[k] = v
		}
		setParamValue(param, hpCharset, p.Charset)
		setParamValue(param, hpName, fileName)
		setParamValue(param, hpBoundary, p.Boundary)
		if mt := mime.FormatMediaType(p.ContentType, param); mt != "" {
			p.ContentType = mt
		}
		p.Header.Set(hnContentType, p.ContentType)
	}
	if p.Disposition != "" {
		// Build disposition header.
		param := make(map[string]string)
		setParamValue(param, hpFilename, fileName)
		if !p.FileModDate.IsZero() {
			setParamValue(param, hpModDate, p.FileModDate.Format(time.RFC822))
		}
		if mt := mime.FormatMediaType(p.Disposition, param); mt != "" {
			p.Disposition = mt
		}
		p.Header.Set(hnContentDisposition, p.Disposition)
	}
	return cte
}

// encodeHeader writes out a sorted list of headers.
func (p *Part) encodeHeader(b *bufio.Writer, wrapWithLFOnly bool, omitLastNewline bool) error {
	keys := make([]string, 0, len(p.Header))
	for k := range p.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for i, k := range keys {
		for j, v := range p.Header[k] {
			encv := v
			switch selectTransferEncoding([]byte(v), true) {
			case TeBase64:
				encv = mime.BEncoding.Encode(utf8, v)
			case TeQuoted:
				encv = mime.QEncoding.Encode(utf8, v)
			}

			var nl string
			if omitLastNewline && i == len(keys)-1 && j == len(p.Header[k])-1 {
				nl = ""
			} else if wrapWithLFOnly {
				nl = "\n"
			} else {
				nl = "\r\n"
			}

			// _ used to prevent early wrapping
			wb := stringutil.Wrap(76, ' ', wrapWithLFOnly, k, ":_", encv, nl)
			wb[len(k)+1] = ' '
			if _, err := b.Write(wb); err != nil {
				return err
			}
		}
	}
	return nil
}

// encodeContent writes out the content in the selected encoding.
func (p *Part) encodeContent(b *bufio.Writer, cte TransferEncoding, useLF bool) (err error) {
	if p.ContentReader != nil {
		return p.encodeContentFromReader(b, useLF)
	}

	if p.parser != nil && p.parser.rawContent {
		cte = TeRaw
	}

	switch cte {
	case TeBase64:
		enc := base64.StdEncoding
		text := make([]byte, enc.EncodedLen(len(p.Content)))
		enc.Encode(text, p.Content)
		// Wrap lines.
		lineLen := 76
		for len(text) > 0 {
			if lineLen > len(text) {
				lineLen = len(text)
			}
			if _, err = b.Write(text[:lineLen]); err != nil {
				return err
			}
			if useLF {
				if _, err := b.Write([]byte{'\n'}); err != nil {
					return err
				}
			} else {
				if _, err := b.Write(crnl); err != nil {
					return err
				}
			}
			text = text[lineLen:]
		}
	case TeQuoted:
		if useLF {
			lfbuf := &bytes.Buffer{}
			qp := quotedprintable.NewWriter(lfbuf)
			if _, err = qp.Write(p.Content); err != nil {
				return err
			}
			err = qp.Close()
			if err != nil {
				return err
			}
			lfbytes := bytes.ReplaceAll(lfbuf.Bytes(), []byte("\r\n"), []byte("\n"))
			if _, err = b.Write(lfbytes); err != nil {
				return err
			}
		} else {
			qp := quotedprintable.NewWriter(b)
			if _, err = qp.Write(p.Content); err != nil {
				return err
			}
			err = qp.Close()
		}
	default:
		_, err = b.Write(p.Content)
	}
	return err
}

// encodeContentFromReader writes out the content read from the reader using base64 encoding.
func (p *Part) encodeContentFromReader(b *bufio.Writer, useLF bool) error {
	text := make([]byte, base64EncodedLineLen) // a single base64 encoded line
	enc := base64.StdEncoding

	chunk := make([]byte, readChunkSize) // contains a whole number of lines
	copy(chunk, p.Content)               // copy the data of the initial read that was issued by `Encode`
	n := len(p.Content)

	for {
		// call read until we get a full chunk / error
		for n < len(chunk) {
			c, err := p.ContentReader.Read(chunk[n:])
			if err != nil {
				if err == io.EOF {
					break
				}
				return err
			}

			n += c
		}

		for i := 0; i < n; i += base64DecodedLineLen {
			size := n - i
			if size > base64DecodedLineLen {
				size = base64DecodedLineLen
			}

			enc.Encode(text, chunk[i:i+size])
			if _, err := b.Write(text[:enc.EncodedLen(size)]); err != nil {
				return err
			}
			if useLF {
				if _, err := b.Write([]byte{'\n'}); err != nil {
					return err
				}
			} else {
				if _, err := b.Write(crnl); err != nil {
					return err
				}
			}
		}

		if n < len(chunk) {
			break
		}

		n = 0
	}

	return nil
}

// selectTransferEncoding scans content for non-ASCII characters and selects 'b' or 'q' encoding.
func selectTransferEncoding(content []byte, quoteLineBreaks bool) TransferEncoding {
	if len(content) == 0 {
		return Te7Bit
	}
	// Binary chars remaining before we choose b64 encoding.
	threshold := b64Percent * len(content) / 100
	bincount := 0
	maxLineLen := 0
	lineLen := 0
	for _, b := range content {
		if b == '\n' || b == '\r' {
			if maxLineLen < lineLen {
				maxLineLen = lineLen
			}
			lineLen = 0
		} else {
			lineLen += 1
		}

		if (b < ' ' || '~' < b) && b != '\t' {
			if !quoteLineBreaks && (b == '\r' || b == '\n') {
				continue
			}
			bincount++
			if bincount >= threshold {
				return TeBase64
			}
		}
	}

	if maxLineLen < lineLen {
		maxLineLen = lineLen
	}

	if bincount == 0 && maxLineLen <= 78 {
		return Te7Bit
	}
	return TeQuoted
}

// setParamValue will ignore empty values
func setParamValue(p map[string]string, k, v string) {
	if v != "" {
		p[k] = v
	}
}
