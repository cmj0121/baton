// Package isolate runs an agent panel inside a container. It is the confinement
// internal/cgroup deliberately is not: cgroup caps what a panel may USE, this one
// narrows what a panel may REACH.
//
// It is a command rewriter and nothing else. A panel's process is described by a
// ptymgr.Spec, so isolating one is a transformation of that spec — `claude --foo`
// in /home/u/proj becomes `docker run … image claude --foo` — and every other
// part of baton keeps working unchanged: the PTY is the container runtime's, so
// input, resize, signals, replay, logging and attention need no special case, and
// no frontend learns that isolation exists.
//
// What it is NOT: a boundary against an agent that is trying to escape. The
// runtime is driven by your uid over your docker socket, and anything that can
// reach that socket can reach the host. It confines an agent that is WRONG, which
// is the failure mode of an unattended fleet; it does not confine one that is
// hostile. docs/ISOLATION.md says so at greater length, and must keep saying so.
//
// Isolation is opt-in per agent profile and off by default, because an isolated
// panel starts slower, debugs harder, and needs an image the user chose. baton
// ships no image: the moment it did, it would own a toolchain matrix it cannot
// maintain.
package isolate

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/limits"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// Mode is the container runtime a panel is isolated with.
type Mode string

// The isolation modes. Docker is the only runtime in this cut because it is the
// one people already have; a second is a follow-on, and the whole package is
// shaped so that adding it is a new binary name and a flag table, not a new
// execution path.
const (
	ModeNone   Mode = "none"   // the default: the panel runs on the host, as today
	ModeDocker Mode = "docker" // the panel runs in a container
)

// Mount is how much of the filesystem crosses into the container.
type Mount string

// The mount policies.
const (
	// MountWorkspace bind-mounts only the panel's working directory. It is the
	// defensible default: the blast radius becomes the tree the agent was pointed
	// at, which is the tree it was going to change anyway.
	MountWorkspace Mount = "workspace"

	// MountHome mounts $HOME as well, which is what an agent CLI that authenticates
	// through a file under $HOME needs to start at all. It is an escape hatch and
	// the docs label it as one: it hands the container everything you have.
	MountHome Mount = "workspace+home"
)

// Network is what the container may reach.
type Network string

// The network policies. There is no egress allowlist here, and that is a scoping
// decision rather than an oversight — filtering by destination is a much larger
// build than choosing between three stances, and it is called out as a non-goal.
const (
	// NetworkHost shares the host's network namespace: the agent reaches its model
	// API, your package registries, and services on your localhost. It is the
	// default because an agent that cannot reach its own API is not an agent.
	NetworkHost Network = "host"

	// NetworkBridge is the runtime's own NAT network: egress works, the host's
	// localhost does not. Worth choosing on macOS, where host networking is not
	// the same feature it is on Linux.
	NetworkBridge Network = "bridge"

	// NetworkNone is no network at all. Honest, and usable only for an agent that
	// needs none — a formatter, a local model, a lint pass.
	NetworkNone Network = "none"
)

// removeTimeout bounds the teardown call. Removing a container is best-effort
// cleanup on a path a person is waiting on (closing a panel, shutting the daemon
// down); a wedged runtime must cost a moment, not the shutdown.
const removeTimeout = 5 * time.Second

// containerHome is $HOME inside the container when the host's is not mounted.
// A container run with --user and a uid the image has no passwd entry for gets
// no home at all, and a tool that writes to an unset $HOME writes to /. Naming
// a writable, throwaway directory is not a policy, it is stopping a papercut.
const containerHome = "/tmp"

// Policy is one agent profile's isolation: the runtime, the image, and what the
// panel is allowed to keep. The zero value isolates nothing, so a profile that
// never mentions isolation behaves exactly as it does today.
type Policy struct {
	Mode     Mode
	Image    string
	Mount    Mount
	Network  Network
	EnvAllow []string
	User     string // "" = the host's own uid:gid; "root" or "1000:1000" to override

	// Invalid, when set, is why this policy could not be read from the config —
	// an unknown runtime, a missing image, a mount policy baton does not offer.
	//
	// It is CARRIED rather than dropped, and that is the whole point of the field.
	// baton's other policies degrade forgivingly: a malformed restart block simply
	// restarts nothing, a bad track-cwd falls back to the default. Isolation cannot
	// do that, because its fallback is a panel running unconfined on the host —
	// which is precisely the outcome the user was writing config to prevent. So a
	// policy that cannot be understood stays enabled and fails the spawn with the
	// reason, rather than quietly becoming no policy at all.
	Invalid string
}

// Enabled reports whether this policy is meant to confine something — including
// a policy that is broken, which must keep failing spawns rather than falling
// back to an unisolated panel. Use Validate to find out whether it can actually
// run.
func (p Policy) Enabled() bool {
	return p.Invalid != "" || (p.Mode != "" && p.Mode != ModeNone)
}

// Validate rejects a policy the runtime could not act on, naming the config key
// the way the user spelled it. It is called when the config loads, so a profile
// that could never spawn is reported at startup rather than at the moment
// somebody presses A — and again at the spawn, which is what refuses to run.
func (p Policy) Validate() error {
	if p.Invalid != "" {
		return errors.New(p.Invalid)
	}
	if !p.Enabled() {
		return nil
	}
	if strings.TrimSpace(p.Image) == "" {
		return fmt.Errorf("isolate: %s needs an image; baton ships none", p.Mode)
	}
	return nil
}

// Runtime is the binary that runs a container for this mode.
func (p Policy) Runtime() string {
	if p.Mode == ModeDocker {
		return "docker"
	}
	return ""
}

// mount is the mount policy with the default filled in.
func (p Policy) mount() Mount {
	if p.Mount == MountHome {
		return MountHome
	}
	return MountWorkspace
}

// network is the network policy with the default filled in.
func (p Policy) network() Network {
	switch p.Network {
	case NetworkBridge, NetworkNone:
		return p.Network
	default:
		return NetworkHost
	}
}

// Wrap rewrites spec to run inside a container named name, folding caps into the
// runtime's own resource flags. A disabled policy returns the spec untouched, so
// the caller has one code path rather than two.
//
// The rewrite is applied to the copy that is LAUNCHED, never to the spec the
// server keeps for respawn — a stored wrap would pin a container name that the
// next run must not reuse. That is the same discipline the session id follows.
func (p Policy) Wrap(name string, spec ptymgr.Spec, caps limits.Limits) (ptymgr.Spec, error) {
	if !p.Enabled() {
		return spec, nil
	}
	if err := p.Validate(); err != nil {
		return spec, err
	}
	command := spec.Command
	if command == "" {
		command = ptymgr.DefaultShell()
	}
	dir := ptymgr.PanelDir(spec.Dir)
	if dir == "" {
		return spec, fmt.Errorf("isolate: a panel with no working directory has nothing to mount")
	}

	args := []string{"run", "--rm", "--init", "--interactive", "--tty", "--name", name}
	args = append(args, p.userArgs()...)
	args = append(args, p.mountArgs(dir)...)
	args = append(args, "--network", string(p.network()))
	capArgs, err := resourceArgs(caps)
	if err != nil {
		return spec, err
	}
	args = append(args, capArgs...)
	args = append(args, p.envArgs(spec.Env, dir)...)
	args = append(args, p.Image, command)
	args = append(args, spec.Args...)

	// Dir stays the host path. The container has its own --workdir; this one is
	// where the RUNTIME CLIENT runs, and keeping it as the panel's directory is
	// what lets everything host-side — the diff pop-up, the git menu, cwd
	// reporting — go on resolving a panel's tree the way it always has.
	//
	// Env is dropped rather than forwarded: it is the host process's environment
	// now, and nothing in it crosses the boundary except through --env above.
	return ptymgr.Spec{Command: p.Runtime(), Args: args, Dir: dir}, nil
}

// userArgs pins the container to the host's own uid and gid unless the profile
// named someone else.
//
// The default is not the image's, and that is the point. A container writing into
// a bind-mounted repo as root leaves root-owned build output in the user's tree —
// a harm that isolation itself would have created, and one they then need sudo to
// undo. An image that genuinely needs root (it installs packages on start) says
// `user: root` and gets it.
func (p Policy) userArgs() []string {
	switch u := strings.TrimSpace(p.User); u {
	case "":
		return []string{"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
	default:
		return []string{"--user", u}
	}
}

// mountArgs renders the bind mounts and the container's working directory.
//
// The workspace is mounted at its OWN absolute path rather than at some tidy
// /workspace. It costs nothing and buys agreement: baton's git tooling runs on the
// host against this same path, so a container that sees a different one would make
// every path in the agent's output — a stack trace, a diff header, a `cd` it
// suggests — wrong on the outside.
func (p Policy) mountArgs(dir string) []string {
	if p.mount() == MountHome {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			// A workspace inside $HOME is already mounted by the home mount; adding it
			// again would be a nested bind of the same tree for no gain.
			if under(dir, home) {
				return []string{"--volume", home + ":" + home, "--workdir", dir}
			}
			return []string{"--volume", home + ":" + home, "--volume", dir + ":" + dir, "--workdir", dir}
		}
	}
	return []string{"--volume", dir + ":" + dir, "--workdir", dir}
}

// envArgs renders what crosses into the container: the terminal type, a usable
// $HOME, whatever env-allow names, and the per-spec variables baton itself
// injected for this panel.
//
// Every variable is passed in the KEY=value form, never the bare KEY that would
// inherit the host's. That is what makes the promise checkable rather than
// hopeful: nothing reaches the container unless something here put it there.
func (p Policy) envArgs(specEnv []string, dir string) []string {
	// ptymgr sets TERM on the host process; that is the runtime client's terminal,
	// not the container's, so the panel's program needs it stated again.
	args := []string{"--env", "TERM=xterm-256color"}

	home := containerHome
	if p.mount() == MountHome {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			home = h
		}
	} else if under(dir, containerHome) {
		home = dir // a workspace under /tmp would otherwise be shadowed by the fallback
	}
	args = append(args, "--env", "HOME="+home)

	for _, name := range p.EnvAllow {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// An unset name passes nothing rather than an empty string: "" and "not set"
		// are different answers to a program reading a credential, and the second is
		// the true one.
		if v, ok := os.LookupEnv(name); ok {
			args = append(args, "--env", name+"="+v)
		}
	}
	for _, kv := range specEnv {
		if strings.Contains(kv, "=") {
			args = append(args, "--env", kv)
		}
	}
	return args
}

// resourceArgs renders a cap policy as the runtime's own resource flags.
//
// The cgroup backend cannot be used here, and the reason is worth stating: it
// would place the runtime CLIENT in the cgroup, and the client is a few hundred
// kilobytes that talk to a daemon. The container is the daemon's child and would
// run entirely uncapped — a limit that reads as applied and holds nothing, which
// is the exact failure docs/LIMITS.md exists to avoid. So the caps are handed to
// the runtime, which puts the container in a cgroup of its own.
//
// A cap the policy leaves unset, or lifts with "unlimited", emits no flag: every
// one of these is unlimited by default, so silence is the correct way to say so.
func resourceArgs(l limits.Limits) ([]string, error) {
	var args []string

	cores, capped, err := limits.ParseCPUs(l.CPUs)
	if err != nil {
		return nil, fmt.Errorf("cpus: %w", err)
	}
	if capped {
		args = append(args, "--cpus="+strconv.FormatFloat(cores, 'f', -1, 64))
	}

	// memory.high has no exact runtime equivalent; --memory-reservation is the
	// nearest thing the runtime offers — a soft limit reclaimed against under
	// pressure rather than a killer — which is what memory-high is for.
	for _, q := range []struct{ flag, field, value string }{
		{"--memory", "memory", l.Memory},
		{"--memory-reservation", "memory-high", l.MemoryHigh},
	} {
		n, capped, err := limits.ParseBytes(q.value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", q.field, err)
		}
		if capped {
			args = append(args, q.flag+"="+strconv.FormatInt(n, 10))
		}
	}

	n, capped, err := limits.ParseCount(l.Pids)
	if err != nil {
		return nil, fmt.Errorf("pids: %w", err)
	}
	if capped {
		args = append(args, "--pids-limit="+strconv.FormatInt(n, 10))
	}

	// nofile is the one cap the cgroup backend has to report as unenforced — an
	// open-file limit is per-process rlimit territory. A container runtime sets
	// rlimits on the process it starts, so under isolation it is enforced for real.
	n, capped, err = limits.ParseCount(l.NOFile)
	if err != nil {
		return nil, fmt.Errorf("nofile: %w", err)
	}
	if capped {
		v := strconv.FormatInt(n, 10)
		args = append(args, "--ulimit=nofile="+v+":"+v)
	}
	return args, nil
}

// Unenforced lists the caps this policy is set to hold but cannot. There are
// none: a container runtime accepts every cap baton offers, nofile included. It
// exists so the spawn path can ask the same question of either backend.
func (p Policy) Unenforced(limits.Limits) []string { return nil }

// Remove tears a container down by name, best effort. It is the teardown the
// `--rm` on the run does not cover: with a TTY attached the runtime does not
// proxy signals, so killing the client — which is what closing a panel or
// shutting the daemon down does — leaves the container running. Without this a
// long day of spawning and closing panels quietly accumulates orphans.
//
// A container that is already gone is the ordinary case, not an error, so nothing
// is reported: the caller has nothing it could do differently.
func Remove(mode Mode, name string) {
	p := Policy{Mode: mode}
	if !p.Enabled() || name == "" {
		return
	}
	cmd := exec.Command(p.Runtime(), "rm", "--force", name)
	if err := cmd.Start(); err != nil {
		return
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(removeTimeout):
		_ = cmd.Process.Kill()
		<-done
	}
}

// ContainerName is the name a panel's container is given: a baton- prefix so a
// stray one is recognisable in `docker ps`, the panel id so it is traceable, and
// a nonce so a respawn cannot collide with a container the previous run left
// behind. The caller records it, because Remove needs the exact name back.
func ContainerName(id string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "baton-" + id // crypto/rand is documented never to fail; a plain name still works
	}
	return "baton-" + id + "-" + hex.EncodeToString(b[:])
}

// ParseMode reads an isolate mode as the config spells it.
func ParseMode(s string) (Mode, bool) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeNone:
		return ModeNone, true
	case ModeDocker:
		return ModeDocker, true
	}
	return ModeNone, false
}

// ParseMount reads a mount policy as the config spells it.
func ParseMount(s string) (Mount, bool) {
	switch Mount(strings.ToLower(strings.TrimSpace(s))) {
	case MountWorkspace:
		return MountWorkspace, true
	case MountHome:
		return MountHome, true
	}
	return MountWorkspace, false
}

// ParseNetwork reads a network policy as the config spells it.
func ParseNetwork(s string) (Network, bool) {
	switch Network(strings.ToLower(strings.TrimSpace(s))) {
	case NetworkHost:
		return NetworkHost, true
	case NetworkBridge:
		return NetworkBridge, true
	case NetworkNone:
		return NetworkNone, true
	}
	return NetworkHost, false
}

// under reports whether path sits inside base (or is base itself), on cleaned
// paths so a trailing slash or a "." segment cannot change the answer.
func under(path, base string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
