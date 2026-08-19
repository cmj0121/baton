package server

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/remote"
)

// Remote access (docs/REMOTE.md). A cockpit on another machine reaches this
// daemon through `ssh <host> baton --stdio`, which bridges the far side's
// stdin/stdout to this session's unix socket. From the server's side that is an
// ordinary connection on its own socket — so what marks it remote is the role it
// declares on hello, exactly as the conductor's fence is marked today.
//
// Remote is OFF by default. Enabling it mints an 8-character passkey that is
// held in memory and never written to disk; a hello declaring roleRemote without
// the current passkey is refused, counted against a rate limiter, and logged.
//
// Be plain about what the passkey buys, because the transport already decided
// the hard part: the far side runs as the fleet owner's uid, and that uid could
// run baton on this machine anyway. The passkey is proof the owner deliberately
// opened the window, and a handle to revoke it. It is not an authentication
// boundary — SECURITY.md draws the same line for the conductor fence.

// roleRemote is the role a cockpit attached over the ssh bridge declares on
// hello. It is otherwise unrestricted: remote exists to be useful, and a
// narrower role can be introduced later without a protocol change.
const roleRemote = "remote"

// roleCockpit is what an undeclared role reads as in the connection list. The
// wire keeps sending "" for the ordinary cockpit; only the display names it.
const roleCockpit = "cockpit"

// sourceLocal labels a connection that came in on the daemon's own socket
// without declaring a source of its own.
const sourceLocal = "local"

// EnableRemote turns remote access on and mints a fresh passkey, returning it.
// Every enable rotates: an old code never survives being switched off and on,
// which is what makes "disable, enable" a complete revocation.
func (s *Server) EnableRemote() (string, error) {
	key, err := remote.NewPasskey()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.remoteOn, s.remoteKey = true, key
	s.mu.Unlock()
	s.remoteLimiter().Reset()
	log.Info().Msg("remote access enabled")
	s.pushRemote()
	return key, nil
}

// RotateRemote mints a new passkey without disturbing the live connections:
// what is already attached stays, and a new attach needs the new code.
func (s *Server) RotateRemote() (string, error) {
	key, err := remote.NewPasskey()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	on := s.remoteOn
	if on {
		s.remoteKey = key
	}
	s.mu.Unlock()
	if !on {
		return "", errRemoteOff
	}
	s.remoteLimiter().Reset()
	log.Info().Msg("remote passkey rotated")
	s.pushRemote()
	return key, nil
}

// DisableRemote turns remote access off, forgets the passkey, and drops every
// connection that came in over the bridge. Local cockpits are untouched.
func (s *Server) DisableRemote() {
	s.mu.Lock()
	s.remoteOn, s.remoteKey = false, ""
	dropped := 0
	for cc := range s.clients {
		if cc.role == roleRemote {
			// Held under mu for the same reason broadcast is: removeClient closes
			// cc.out under this lock, so a send outside it can race a detach and
			// write to a closed channel.
			send(cc, goodbye("remote access was disabled on the fleet"))
			dropped++
		}
	}
	s.mu.Unlock()
	log.Info().Int("dropped", dropped).Msg("remote access disabled")
	s.pushRemote()
}

// RemoteEnabled reports whether remote access is currently on.
func (s *Server) RemoteEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remoteOn
}

// remoteLimiter returns the shared failed-attempt limiter, building it on first
// use so a server constructed without the option still rate-limits.
func (s *Server) remoteLimiter() *remote.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteLim == nil {
		s.remoteLim = remote.NewLimiter(0, 0)
	}
	return s.remoteLim
}

// admitRemote decides whether a hello declaring roleRemote may attach. It
// returns the refusal to send the client, or "" to let it through.
//
// Every refusal counts against the limiter, "remote is off" included: telling a
// prober the difference between a closed door and a wrong key for free is worth
// less than making them wait either way.
func (s *Server) admitRemote(cmd proto.Command, source string) string {
	lim := s.remoteLimiter()
	if !lim.Allow() {
		log.Warn().Str("source", source).Msg("remote attach refused: too many failed attempts")
		return "too many failed attempts; wait a minute and try again"
	}

	s.mu.Lock()
	on, key := s.remoteOn, s.remoteKey
	s.mu.Unlock()

	switch {
	case !on:
		n := lim.Fail()
		log.Warn().Str("source", source).Int("failures", n).Msg("remote attach refused: remote access is not enabled")
		return "remote access is not enabled on this fleet"
	case !remote.EqualPasskey(key, cmd.Passkey):
		n := lim.Fail()
		log.Warn().Str("source", source).Int("failures", n).Msg("remote attach refused: wrong passkey")
		return "wrong passkey"
	}

	lim.Reset() // a correct code clears the slate; the failures it forgets were against a code that opened nothing
	log.Info().Str("source", source).Msg("remote cockpit attached")
	return ""
}

// remoteInfoFor builds the status one connection sees. The passkey is filled in
// for a local connection only: a remote cockpit may list and kick, but the code
// that admits the NEXT one is the owner's to read on the fleet's own machine.
func (s *Server) remoteInfoFor(asker *clientConn) *proto.RemoteInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remoteInfoForLocked(asker)
}

// remoteInfoForLocked is remoteInfoFor with s.mu already held. It exists so the
// push loop can build every connection's view and send it inside ONE critical
// section — sending outside the lock would race removeClient closing cc.out.
func (s *Server) remoteInfoForLocked(asker *clientConn) *proto.RemoteInfo {
	local := asker == nil || asker.role != roleRemote
	info := &proto.RemoteInfo{Enabled: s.remoteOn, Local: local}
	if local {
		info.Passkey = s.remoteKey
	}
	for cc := range s.clients {
		if !cc.greeted {
			continue // still mid-handshake; it has no role or source to list yet
		}
		info.Conns = append(info.Conns, proto.RemoteConn{
			ID:     cc.id,
			Source: connSource(cc),
			Role:   connRole(cc),
			Since:  cc.since.Format(time.RFC3339),
			Self:   cc == asker,
			Remote: cc.role == roleRemote,
		})
	}
	// A stable order, oldest first, so the list does not reshuffle under the
	// cursor between refreshes — map iteration alone would.
	sort.Slice(info.Conns, func(i, j int) bool {
		if info.Conns[i].Since != info.Conns[j].Since {
			return info.Conns[i].Since < info.Conns[j].Since
		}
		return info.Conns[i].ID < info.Conns[j].ID
	})
	return info
}

// connSource is the label a connection is listed under: what it called itself on
// hello, or "local" when it declared nothing.
func connSource(cc *clientConn) string {
	if cc.source != "" {
		return cc.source
	}
	return sourceLocal
}

// connRole is a connection's role for display; an undeclared role is the
// ordinary full-power cockpit.
func connRole(cc *clientConn) string {
	if cc.role == "" {
		return roleCockpit
	}
	return cc.role
}

// pushRemote sends every attached client the status as IT should see it. It is
// called after any change — enable, rotate, disable, kick, attach, detach — so
// an open overlay on either side of the pipe is never stale.
func (s *Server) pushRemote() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for cc := range s.clients {
		send(cc, proto.ServerMsg{Type: "remote", Remote: s.remoteInfoForLocked(cc)})
	}
}

// kickConn drops the connection with the given id, telling it why. It reports
// whether anything matched, so the caller can say "no such connection" rather
// than silently succeeding.
func (s *Server) kickConn(id, by string) bool {
	s.mu.Lock()
	found := false
	for cc := range s.clients {
		if cc.id == id {
			send(cc, goodbye("this connection was kicked from "+by))
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return false
	}
	log.Info().Str("conn", id).Str("by", by).Msg("connection kicked")
	s.pushRemote()
	return true
}

// goodbye is the server dropping a connection on purpose, with the reason. The
// teardown rides the message: the writer goroutine closes the socket once it has
// encoded a "goodbye", so the far cockpit is always told why rather than just
// finding its socket gone.
func goodbye(reason string) proto.ServerMsg {
	return proto.ServerMsg{Type: "goodbye", Error: reason}
}

// errRemoteOff is what a rotate asks for when nothing is enabled: there is no
// code to replace, and minting one silently would be an enable in disguise.
var errRemoteOff = errors.New("remote access is not enabled")

// nextConnID mints the stable id a connection is listed and kicked by. It is a
// plain counter rather than the socket's address because every connection comes
// in on the same unix socket — the address distinguishes nothing.
func (s *Server) nextConnID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connSeq++
	return fmt.Sprintf("c%d", s.connSeq)
}

// onRemoteControl runs the three operator actions behind the remote overlay.
//
// They are LOCAL ONLY, which is the one asymmetry in the whole feature: a remote
// cockpit may list connections and kick any of them, including its own, but the
// passkey — the thing that decides who gets in next — is changed on the machine
// the fleet runs on. Anyone holding a live remote attach already proved they had
// the current code; letting them mint the next one would turn one window into a
// permanent one.
func (s *Server) onRemoteControl(cc *clientConn, action string) {
	if cc.role == roleRemote {
		send(cc, proto.ServerMsg{Type: "error", Error: "the passkey is changed on the fleet's own machine, not over a remote attach"})
		return
	}
	switch action {
	case "remote.enable":
		if _, err := s.EnableRemote(); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
		}
	case "remote.rotate":
		if _, err := s.RotateRemote(); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
		}
	case "remote.disable":
		s.DisableRemote()
	}
}

// applyRemoteSetting acts on the config's `settings.remote` on boot and on every
// reload — but only when the FILE changed. A cockpit that pressed `C-t @` since
// the last reload has made a decision the daemon should not quietly undo the
// next time an unrelated key is edited, so the switch follows the transition
// rather than the value.
func (s *Server) applyRemoteSetting(want bool) {
	s.mu.Lock()
	changed := want != s.remoteCfg
	s.remoteCfg = want
	on := s.remoteOn
	s.mu.Unlock()

	switch {
	case !changed:
		return
	case want && !on:
		if _, err := s.EnableRemote(); err != nil {
			log.Warn().Err(err).Msg("settings.remote: could not mint a passkey; remote stays off")
		}
	case !want && on:
		s.DisableRemote()
	}
}
