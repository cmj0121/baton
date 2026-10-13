// Package classifier is baton's client for a MOLD classifier serving on a unix
// socket: the fourth source of attention, a trained bundle asked whether a
// quiet panel's tail reads like a wait for the human.
//
// It speaks mold's own protocol — one JSON line per request on a unix socket —
// so a fleet that has no mold has nothing to install and nothing to configure:
// the socket is absent, the client says so once, and the regex decides as it
// did before. That is the whole deal with D1: no binary, no bundle, no module.
//
// Two things the package is deliberate about, both from D9 and D12:
//
//   - It dials per ask. A unix dial is microseconds beside a 60 ms ask, and a
//     held connection would have to notice `mold serve --stop` and a restart
//     and reconnect; a fresh dial notices for free.
//   - It latches on the first failure. A dead socket must not log every tick,
//     and must not need a daemon restart to come back: the first dial error,
//     timeout or malformed line is logged once and the client reports Absent
//     until a config reload builds a fresh client or a Status succeeds (which
//     is what `baton ctl classifier status` is for). A reply from the wrong
//     scope is not a failure of the socket — mold is alive, just untrained for
//     us — so it is logged once too, but counts as "no" rather than tripping
//     the latch.
//
// What it does NOT do is gate on the panel's state, queue, retry or pool. The
// Monitor owns when to ask and what to do with the answer; this package owns
// one bounded dial and the truth about whether anybody answered.
package classifier

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/cmj0121/baton/internal/scrub"
)

// Key is the one key the bundle is trained on (D3). A reply whose top answer
// carries any other key is a "no", not an error: mold resolves scopes by
// meaning, so a bundle without the configured scope answers from a different
// one, and the key is how a borrowed answer is told apart from a real one.
const Key = "attention"

// errorRunes caps what of mold's own error text reaches the log. The peer is
// trusted the way any process of this uid is, but a log line is not the place
// for a megabyte with newlines in it, whoever wrote it.
const errorRunes = 200

// maxLineBytes bounds a single reply line. A status line is a few hundred
// bytes and an answer a few dozen; a megabyte is a server that is not mold.
const maxLineBytes = 1 << 20

// ErrAbsent is what Ask returns while the latch is set: the socket was found
// wanting once already, and the client is not going to dial again until a
// reload builds a fresh client or a Status probe succeeds. Callers treat it as "regex decides", never as news.
var ErrAbsent = errors.New("classifier: absent")

// Config is the resolved shape of the `panel.classifier` block — paths
// expanded, numbers parsed — as the config layer hands it over.
type Config struct {
	// Socket is the unix socket mold serves on.
	Socket string
	// Scope is the mold scope that holds the one key; a reply from any other
	// scope is not an answer (D9).
	Scope string
	// MinConfidence is baton's gate, 0–1. It is NOT sent to mold: the raw score
	// comes back so a status probe can show it, and Answer applies the floor.
	MinConfidence float64
	// Timeout bounds the whole ask — dial, write and read together.
	Timeout time.Duration
}

// Answer is what one ask says about one tail. Attention is true only when the
// reply came from the configured scope, its top key is Key, and the confidence
// clears MinConfidence; Confidence is the raw score in every case, so a near
// miss is visible to whoever is tuning the floor.
type Answer struct {
	Attention  bool
	Confidence float64
}

// Status is mold's `{"op":"status"}` reply, field for field.
type Status struct {
	PID     int    `json:"pid"`
	Version string `json:"version"`
	Bundle  string `json:"bundle"`
	Keys    int    `json:"keys"`
	Device  string `json:"device"`
}

// Client asks one mold socket one question at a time. It is safe for use from
// several goroutines; the latch is the only shared state and it sits under mu.
type Client struct {
	cfg  Config
	logf func(format string, args ...any)

	mu          sync.Mutex
	absent      bool
	scopeWarned bool
}

// New builds a client for cfg. logf receives the one line a failure kind gets
// before the latch closes; nil is accepted and logs nothing.
func New(cfg Config, logf func(format string, args ...any)) *Client {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Client{cfg: cfg, logf: logf}
}

// Config returns the configuration the client was built with, for the status
// verb to print beside what the socket said.
func (c *Client) Config() Config { return c.cfg }

// Absent reports whether the latch is set: a failure has been seen and logged,
// and Ask will not dial again until a reload builds a fresh client or a Status
// probe succeeds.
func (c *Client) Absent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.absent
}

// Ask sends text to the configured scope and reports whether the bundle calls
// it attention. While the latch is set it returns ErrAbsent without dialing.
// Any other error has already been logged once and has set the latch, so the
// caller has nothing to log and nothing to retry.
func (c *Client) Ask(ctx context.Context, text string) (Answer, error) {
	if c.Absent() {
		return Answer{}, ErrAbsent
	}
	req := struct {
		Op    string `json:"op"`
		Text  string `json:"text"`
		Scope string `json:"scope"`
		Top   int    `json:"top"`
	}{Op: "ask", Text: text, Scope: c.cfg.Scope, Top: 1}
	var reply struct {
		Scope   string `json:"scope"`
		Error   string `json:"error"`
		Answers []struct {
			Key        string  `json:"key"`
			Confidence float64 `json:"confidence"`
		} `json:"answers"`
	}
	if err := c.exchange(ctx, req, &reply); err != nil {
		return Answer{}, c.trip(err)
	}
	if reply.Error != "" {
		// mold declined in its own words — a bundle it will not serve, a request
		// it will not read. It would decline every tick the same way, so it is a
		// failure kind like a dead socket, not an empty answer.
		return Answer{}, c.trip(fmt.Errorf("mold: %s", scrub.Capped(reply.Error, errorRunes)))
	}
	if len(reply.Answers) == 0 {
		return Answer{}, nil
	}
	top := reply.Answers[0]
	if reply.Scope != c.cfg.Scope {
		// mold answered, so the socket is alive and the latch stays open — but
		// it answered from a bundle that does not hold our scope, and a score
		// from somebody else's question is not an answer to ours (D9). Said
		// once: it recurs every tick until the operator trains the scope.
		c.mu.Lock()
		first := !c.scopeWarned
		c.scopeWarned = true
		c.mu.Unlock()
		if first {
			c.logf("classifier: asked scope %q, answered from %q (socket %s); its answers do not count", c.cfg.Scope, reply.Scope, c.cfg.Socket)
		}
		return Answer{Confidence: top.Confidence}, nil
	}
	return Answer{
		Attention:  top.Key == Key && top.Confidence >= c.cfg.MinConfidence,
		Confidence: top.Confidence,
	}, nil
}

// Status asks mold about itself. It always dials, latch or not, and a success
// clears the latch: a probe that finds the socket alive is the evidence the
// latch was waiting for. A failure sets it, logged once as Ask would.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	if err := c.exchange(ctx, struct {
		Op string `json:"op"`
	}{Op: "status"}, &st); err != nil {
		return Status{}, c.trip(err)
	}
	c.mu.Lock()
	c.absent = false
	c.mu.Unlock()
	return st, nil
}

// trip sets the latch and returns err. The log line is written only on the
// transition, so a socket that is gone for the afternoon costs one line, and
// the line names the socket because that is what an operator checks first.
func (c *Client) trip(err error) error {
	c.mu.Lock()
	first := !c.absent
	c.absent = true
	c.mu.Unlock()
	if first {
		c.logf("classifier: %v (socket %s); treating it as absent until reload or a status probe", err, c.cfg.Socket)
	}
	return err
}

// exchange is one round trip: dial, write req as a line, read one line into
// out, close. The deadline covers all of it, from whichever of ctx and Timeout
// expires first, so a mold that accepts and then stalls is a timeout rather
// than a hung Monitor worker.
func (c *Client) exchange(ctx context.Context, req, out any) error {
	if c.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
		defer cancel()
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.cfg.Socket)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return err
		}
	}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	r := bufio.NewReaderSize(conn, 4096)
	resp, err := readLine(r)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if err := json.Unmarshal(resp, out); err != nil {
		return fmt.Errorf("malformed reply: %w", err)
	}
	return nil
}

// readLine reads up to and excluding the first newline, refusing a line past
// maxLineBytes rather than buffering whatever is on the other end.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, more, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, chunk...)
		if len(line) > maxLineBytes {
			return nil, fmt.Errorf("reply exceeds %d bytes", maxLineBytes)
		}
		if !more {
			return line, nil
		}
	}
}
