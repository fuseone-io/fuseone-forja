package model

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"sync"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// maxEventBytes bounds what the meter keeps of one SSE line or event. The
// two events it reads carry a few numbers; anything longer is content the
// meter has no use for.
const maxEventBytes = 64 << 10

// maxJSONBytes bounds the body of a response that is not streamed. A
// message whose output fills max_tokens is far below it.
const maxJSONBytes = 16 << 20

// wireUsage is the usage block of a Messages API response, as message_start
// carries it and as a response that is not streamed carries it at the top
// level. OutputTokens is a pointer because message_delta may leave it out.
type wireUsage struct {
	InputTokens              int64  `json:"input_tokens"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
}

func (w wireUsage) tokens() domain.TokenUsage {
	u := domain.TokenUsage{
		Input:         max(w.InputTokens, 0),
		CacheCreation: max(w.CacheCreationInputTokens, 0),
		CacheRead:     max(w.CacheReadInputTokens, 0),
	}
	if w.OutputTokens != nil {
		u.Output = max(*w.OutputTokens, 0)
	}
	return u
}

// parser reads usage out of a body fed to it in pieces.
type parser interface {
	feed(p []byte)
	usage() domain.TokenUsage
}

// parserFor picks the reader by the response's media type. A body that is
// neither a stream nor JSON carries no usage.
func parserFor(contentType string) parser {
	media, _, _ := mime.ParseMediaType(contentType)
	switch media {
	case "text/event-stream":
		return &sseParser{}
	case "application/json":
		return &jsonParser{}
	}
	return noUsage{}
}

type noUsage struct{}

func (noUsage) feed([]byte)              {}
func (noUsage) usage() domain.TokenUsage { return domain.TokenUsage{} }

// sseParser follows the events of a stream: message_start sets the input
// and a first output count, message_delta replaces the output count with
// its own, which is the message's running total.
type sseParser struct {
	line     []byte
	data     []byte
	overflow bool
	seen     domain.TokenUsage
}

func (s *sseParser) feed(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.keep(p)
			return
		}
		s.keep(p[:i])
		s.endLine()
		p = p[i+1:]
	}
}

func (s *sseParser) keep(p []byte) {
	if len(s.line)+len(p) > maxEventBytes {
		s.overflow = true
		return
	}
	s.line = append(s.line, p...)
}

// endLine handles one complete line: a data line adds to the event, a
// blank line ends it.
func (s *sseParser) endLine() {
	line := bytes.TrimSuffix(s.line, []byte("\r"))
	overflow := s.overflow
	s.line, s.overflow = s.line[:0], false
	switch {
	case len(line) == 0:
		s.event()
	case overflow:
		s.data = s.data[:0]
	case bytes.HasPrefix(line, []byte("data:")):
		value := bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))
		if len(s.data) > 0 {
			s.data = append(s.data, '\n')
		}
		s.data = append(s.data, value...)
	}
}

func (s *sseParser) event() {
	data := s.data
	s.data = s.data[:0]
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Usage wireUsage `json:"usage"`
		} `json:"message"`
		Usage wireUsage `json:"usage"`
	}
	if len(data) == 0 || json.Unmarshal(data, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		s.seen = s.seen.Add(ev.Message.Usage.tokens())
	case "message_delta":
		if ev.Usage.OutputTokens != nil {
			s.seen.Output = max(*ev.Usage.OutputTokens, 0)
		}
	}
}

// usage counts an event the stream was cut in the middle of, if the data
// it had is whole.
func (s *sseParser) usage() domain.TokenUsage {
	if len(s.line) > 0 && !s.overflow {
		s.endLine()
	}
	if len(s.data) > 0 {
		s.event()
	}
	return s.seen
}

// jsonParser keeps the body and reads its top-level usage at the end.
type jsonParser struct {
	body     []byte
	overflow bool
}

func (j *jsonParser) feed(p []byte) {
	if len(j.body)+len(p) > maxJSONBytes {
		j.overflow = true
		return
	}
	j.body = append(j.body, p...)
}

func (j *jsonParser) usage() domain.TokenUsage {
	var msg struct {
		Usage wireUsage `json:"usage"`
	}
	if j.overflow || json.Unmarshal(j.body, &msg) != nil {
		return domain.TokenUsage{}
	}
	return msg.Usage.tokens()
}

// meteredBody hands every byte the client is sent to the parser and
// records the usage once, at the end of the body or when it is closed
// early, whichever comes first.
type meteredBody struct {
	io.ReadCloser
	parser parser
	record func(domain.TokenUsage)
	once   sync.Once
}

func (b *meteredBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.parser.feed(p[:n])
	}
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *meteredBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish()
	return err
}

func (b *meteredBody) finish() {
	b.once.Do(func() { b.record(b.parser.usage()) })
}
