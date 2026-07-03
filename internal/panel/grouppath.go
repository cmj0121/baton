package panel

import "strings"

// Group paths. A work item's name is a slash-delimited path, so a group can nest
// inside another: a panel with Group "backend/api" sits in the leaf group
// "backend/api", nested under "backend". Membership stays derived — a group at a
// path "exists" while any panel carries that path or one under it — so these helpers
// are the whole vocabulary: parent/leaf/top segments, the subtree test that replaces
// every literal `p.Group == name`, and the immediate-child lookup the split descends
// by. The empty path "" means ungrouped and is never a group.

// GroupSep separates the segments of a group path.
const GroupSep = "/"

// GroupParent is the path one level up — the group that directly contains this one,
// or "" for a top-level group (and for the empty path).
func GroupParent(path string) string {
	i := strings.LastIndex(path, GroupSep)
	if i < 0 {
		return ""
	}
	return path[:i]
}

// GroupLeaf is the last segment — the group's own name within its parent. For a
// top-level group it is the whole path; for "" it is "".
func GroupLeaf(path string) string {
	i := strings.LastIndex(path, GroupSep)
	if i < 0 {
		return path
	}
	return path[i+1:]
}

// GroupTop is the first segment — the top-level group a path belongs to, the unit
// the dashboard folds by. "" for the empty path.
func GroupTop(path string) string {
	if i := strings.Index(path, GroupSep); i >= 0 {
		return path[:i]
	}
	return path
}

// GroupIsUnder reports whether path p lies in the subtree rooted at anc — p is anc
// itself or any descendant. This is the subtree query group-wide operations recurse
// over, in place of the flat `p.Group == anc`. A non-empty anc only; the empty
// ancestor (ungrouped) roots nothing.
func GroupIsUnder(anc, p string) bool {
	if anc == "" {
		return false
	}
	return p == anc || strings.HasPrefix(p, anc+GroupSep)
}

// GroupChildSegment returns the segment of path that sits immediately under parent —
// the child of parent on the way down to path — and whether path is a strict
// descendant of parent at all. With parent "" the child is path's top segment, so
// the split can descend from the dashboard's top level down through the tree.
//
//	GroupChildSegment("backend/api/x", "backend") == ("api", true)
//	GroupChildSegment("backend/api",   "")        == ("backend", true)
//	GroupChildSegment("backend",       "backend") == ("", false)  // not a descendant
func GroupChildSegment(path, parent string) (string, bool) {
	if parent != "" {
		// A strict descendant of parent has parent+"/" as a prefix; that alone rules
		// out path == parent and unrelated paths.
		if !strings.HasPrefix(path, parent+GroupSep) {
			return "", false
		}
		return GroupTop(path[len(parent)+len(GroupSep):]), true
	}
	if path == "" {
		return "", false
	}
	return GroupTop(path), true
}

// GroupJoin appends a child segment to a parent path, yielding parent/seg (or seg
// when parent is "" / top level).
func GroupJoin(parent, seg string) string {
	if parent == "" {
		return seg
	}
	return parent + GroupSep + seg
}

// GroupValid reports whether path is a well-formed group path: non-empty with no
// empty segments (no leading, trailing, or doubled separators). The empty path is
// "ungrouped", not a group, so it is not valid here.
func GroupValid(path string) bool {
	if path == "" {
		return false
	}
	for _, seg := range strings.Split(path, GroupSep) {
		if seg == "" {
			return false
		}
	}
	return true
}
