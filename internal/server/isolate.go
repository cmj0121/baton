package server

import (
	"fmt"
	"strings"
	"sync"
	"syscall"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/isolate"
	"github.com/cmj0121/baton/internal/limits"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// isolateSpec rewrites a panel's spec to run inside a container and records the
// name it was launched under, so teardown can find it again.
//
// There is no fallback. A policy that cannot be rendered — an unknown runtime, a
// missing image, a cap the runtime would not take — fails the spawn with that
// reason, because the alternative is a panel running on the host for a profile
// whose whole purpose was that it should not. The runtime's own failures (no
// docker, no such image) arrive the same way: as the spawn error, or as the
// runtime's message in the panel, never as a quiet unisolated start.
func (s *Server) isolateSpec(id string, pol isolate.Policy, spec *ptymgr.Spec, caps limits.Limits) error {
	name := isolate.ContainerName(id)
	wrapped, err := pol.Wrap(name, withoutFleetSocket(*spec), caps)
	if err != nil {
		return fmt.Errorf("isolate panel %s: %w", id, err)
	}
	s.mu.Lock()
	s.containers[id] = name
	s.mu.Unlock()
	*spec = wrapped
	log.Info().Str("panel", id).Str("runtime", string(pol.Mode)).Str("image", pol.Image).
		Str("container", name).Msg("spawning an isolated panel")
	return nil
}

// withoutFleetSocket drops BATON_SOCK from a spec's env before it crosses into a
// container.
//
// An isolated agent cannot drive the fleet, and it should not be handed a path
// that says it can: the socket lives on the host and is not mounted, so the
// variable would point at nothing and turn "you were not given this" into a
// confusing connection failure. The panel id stays — an agent still benefits from
// knowing which panel it is, even when it cannot act on the answer.
//
// This is a real loss of function and the docs say so. It is also the correct
// direction: a conductor that could reach the socket from inside a container
// would be isolated from the filesystem and not from the fleet.
func withoutFleetSocket(spec ptymgr.Spec) ptymgr.Spec {
	kept := make([]string, 0, len(spec.Env))
	for _, kv := range spec.Env {
		if strings.HasPrefix(kv, paths.EnvSocket+"=") {
			continue
		}
		kept = append(kept, kv)
	}
	spec.Env = kept
	return spec
}

// signalPanel delivers a signal to a panel, choosing where it has to land.
//
// An unisolated panel takes it on its process group, the way it always has. An
// isolated one cannot: that group is the runtime client, so a SIGINT meant to
// interrupt the agent's current job would end the client and take the panel with
// it — a different action from the one the key asked for. The runtime hands it to
// the container's PID 1 instead.
//
// A failed delivery is reported and NOT retried against the client, for the same
// reason: the fallback is a strictly different action. Closing the panel already
// force-removes the container, so "make it stop" still has a working key.
func (s *Server) signalPanel(id, name string, sig syscall.Signal) {
	s.mu.Lock()
	container := s.containers[id]
	mode := s.isolationModeLocked(id)
	s.mu.Unlock()

	if container == "" {
		s.pty.Signal(id, sig)
		return
	}
	if err := isolate.Signal(mode, container, name); err != nil {
		log.Warn().Str("panel", id).Str("container", container).Str("signal", name).Err(err).
			Msg("signalling an isolated panel failed")
	}
}

// releaseContainer tears down the container a panel was launched in, if it had
// one, and forgets the name. It is safe to call for a panel that was never
// isolated and safe to call twice: the name is taken under the lock, so only the
// first caller has anything to remove.
//
// It is needed at all because `--rm` only covers a container that exits on its
// own. With a TTY attached the runtime does not proxy signals, so every way baton
// ends a panel — a close, a kill, a shutdown — ends the CLIENT and leaves the
// container running. Without this, a day of spawning and closing panels quietly
// accumulates orphans.
func (s *Server) releaseContainer(id string) {
	s.mu.Lock()
	name := s.containers[id]
	delete(s.containers, id)
	mode := s.isolationModeLocked(id)
	s.mu.Unlock()
	if name == "" {
		return
	}
	// Off the caller's path: teardown talks to a daemon over a socket, and a panel
	// closing must not wait on it.
	go func() {
		isolate.Remove(mode, name)
		log.Debug().Str("panel", id).Str("container", name).Msg("removed a panel's container")
	}()
}

// isolationModeLocked is the runtime a panel's container was started with,
// resolved from the profile it recorded. Caller holds s.mu.
func (s *Server) isolationModeLocked(id string) isolate.Mode {
	mode := s.agentIsolate[s.specs[id].Profile].Mode
	if mode == "" {
		// The profile's isolation was reloaded away under a live panel. The container
		// is still there and still ours, so fall back to the only runtime baton has
		// rather than leaking it — a wrong guess costs a failed removal, a missing
		// one costs a container that outlives the daemon.
		mode = isolate.ModeDocker
	}
	return mode
}

// sweepContainers removes every recorded container at once, the shutdown pass
// that the per-panel teardown cannot cover: KillAll ends the runtime clients
// without touching what they were attached to.
//
// Removals run concurrently and are waited for. A fleet of twenty would otherwise
// serialise into twenty round-trips on the way out, and the daemon exiting before
// they land is exactly the orphan this exists to prevent.
func (s *Server) sweepContainers() {
	s.mu.Lock()
	names := make(map[string]isolate.Mode, len(s.containers))
	for id, name := range s.containers {
		names[name] = s.isolationModeLocked(id)
	}
	s.containers = make(map[string]string)
	s.mu.Unlock()
	if len(names) == 0 {
		return
	}

	var wg sync.WaitGroup
	for name, mode := range names {
		wg.Add(1)
		go func(mode isolate.Mode, name string) {
			defer wg.Done()
			isolate.Remove(mode, name)
		}(mode, name)
	}
	wg.Wait()
	log.Info().Int("containers", len(names)).Msg("removed isolated panels' containers on shutdown")
}
