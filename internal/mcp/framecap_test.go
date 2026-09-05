package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// legitimateFrame is the size of the largest frame a real client sends, written
// as a figure rather than as maxFrameBytes-minus-something. A test that sizes its
// input off the constant it is checking moves with the constant and can never
// catch a cap that has been narrowed to where it bites.
//
// A quarter of a mebibyte: baton's MCP tools carry panel ids, an agent name, a
// directory and a prompt, and a prompt that arrived as an argv string is already
// bounded near this by the operating system.
const legitimateFrame = 256 << 10

// paddedPing is a perfectly valid ping of exactly n bytes, padded out with a
// member nothing reads. Both halves of the cap are driven with it: over the cap it
// is what a frame the server MUST refuse looks like — well-formed, correctly
// framed, and only too big — and under it, one the server must answer.
func paddedPing(id string, n int) string {
	head := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"ping","pad":"`, id)
	const tail = `"}`
	return head + strings.Repeat("p", n-len(head)-len(tail)) + tail
}

// decodeResponses splits the server's output into JSON-RPC responses.
func decodeResponses(t *testing.T, out []byte) []rpcResponse {
	t.Helper()
	var got []rpcResponse
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("decode a %d-byte reply: %v", len(line), err)
		}
		got = append(got, resp)
	}
	return got
}

// TestOversizedFrameIsRefusedAndTheStreamSurvives drives a frame past the cap and
// checks it is refused, and that the NEXT call still works.
//
// The oversized frame is a VALID ping: remove the cap and the server answers it
// with its own id, which is what this asserts against. Unbounded, one 256 MiB line
// took the process to 519 MiB of heap and was swallowed without a word — and it is
// the process the conductor drives the whole fleet through.
//
// The second half is the other point. Truncating instead of refusing would be
// worse than either failure: the tail of a dropped frame read as the head of the
// next one desynchronises every call after it.
func TestOversizedFrameIsRefusedAndTheStreamSurvives(t *testing.T) {
	in := strings.NewReader(
		paddedPing("41", maxFrameBytes+1) + "\n" +
			`{"jsonrpc":"2.0","id":7,"method":"ping"}` + "\n")

	var out bytes.Buffer
	if err := New("test").Serve(in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	got := decodeResponses(t, out.Bytes())
	if len(got) != 2 {
		t.Fatalf("got %d responses, want the refusal and the ping", len(got))
	}
	if got[0].Error == nil || got[0].Error.Code != -32700 {
		t.Fatalf("the oversized frame should be refused, got %+v", got[0])
	}
	if !strings.Contains(got[0].Error.Message, fmt.Sprint(maxFrameBytes)) {
		t.Errorf("the refusal should name the limit, got %q", got[0].Error.Message)
	}
	if string(got[0].ID) != "null" {
		t.Errorf("id = %s, want null — it was in the bytes that were dropped", got[0].ID)
	}
	if string(got[1].ID) != "7" || got[1].Error != nil {
		t.Fatalf("the call after the refusal should be answered normally, got %+v", got[1])
	}
}

// TestLargeFrameUnderTheCapIsServed is the other half: a real client's largest
// frame has to be served. A cap that fired on a genuine tool call would strand the
// conductor rather than protect it.
func TestLargeFrameUnderTheCapIsServed(t *testing.T) {
	var out bytes.Buffer
	if err := New("test").Serve(strings.NewReader(paddedPing("7", legitimateFrame)+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	got := decodeResponses(t, out.Bytes())
	if len(got) != 1 || string(got[0].ID) != "7" || got[0].Error != nil {
		t.Fatalf("a %d-byte frame should be served, got %d responses", legitimateFrame, len(got))
	}
}

// TestOversizedFrameAtEOF: an oversized frame that ends at EOF with no newline
// must be refused once and end the loop, not spin.
func TestOversizedFrameAtEOF(t *testing.T) {
	var out bytes.Buffer
	unterminated := paddedPing("41", maxFrameBytes+1)
	if err := New("test").Serve(strings.NewReader(unterminated), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	got := decodeResponses(t, out.Bytes())
	if len(got) != 1 || got[0].Error == nil {
		t.Fatalf("want exactly one refusal, got %d responses", len(got))
	}
}
