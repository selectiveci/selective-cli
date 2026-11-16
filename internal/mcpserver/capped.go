package mcpserver

import "bytes"

type capped struct {
	limit     int
	head      []byte
	total     int
	line      []byte
	lineStart int
	inLine    bool
	overflow  bool
	last      []byte
	lastStart int
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.limit - len(c.head); room > 0 {
		c.head = append(c.head, p[:min(room, len(p))]...)
	}
	for len(p) > 0 {
		end := len(p)
		if i := bytes.IndexByte(p, '\n'); i >= 0 {
			end = i + 1
		}
		c.extend(p[:end])
		if p[end-1] == '\n' {
			c.endLine()
		}
		c.total += end
		p = p[end:]
	}
	return n, nil
}

func (c *capped) extend(piece []byte) {
	if !c.inLine {
		c.inLine = true
		c.lineStart = c.total
	}
	if c.overflow || len(c.line)+len(piece) > c.limit {
		c.overflow = true
		c.line = nil
		return
	}
	c.line = append(c.line, piece...)
}

func (c *capped) endLine() {
	switch {
	case c.overflow:
		c.last = nil
	case len(bytes.TrimSpace(c.line)) > 0:
		c.last = c.line
		c.lastStart = c.lineStart
	}
	c.line, c.inLine, c.overflow = nil, false, false
}

func (c *capped) result(cut, timedOut bool) ([]byte, bool) {
	if c.total <= c.limit && !cut && !timedOut {
		return c.head, false
	}
	out := c.head
	if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
		out = out[:i+1]
	} else {
		out = nil
	}
	if c.last != nil && c.lastStart >= len(out) {
		out = append(out[:len(out):len(out)], c.last...)
	}
	return out, cut || c.total > c.limit || len(out) < c.total
}
