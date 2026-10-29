package app

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"unicode/utf8"
)

// Topology-graph canvases. A canvas is a user-authored layout of the Nodes
// topology: node positions plus per-edge bends (waypoints) and label
// positions. Built-in automatic layouts are computed in the browser and are
// never stored; only custom canvases live here. Shared by every session and
// pushed to open browsers over SSE.

// Limits on stored canvases. They bound the config file and the SSE payload;
// a sane mesh stays far below them.
const (
	MaxGraphCanvases     = 32
	MaxGraphCanvasNodes  = 2000
	MaxGraphCanvasEdges  = 4000
	MaxGraphWaypoints    = 8
	MaxGraphCanvasName   = 64 // runes
	maxGraphKeyLen       = 512
	maxGraphCoord        = 1e6
	maxGraphWaypointRel  = 100
	DefaultGraphCanvasID = "default"
)

var graphCanvasIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// GraphWaypoint is a bend of an edge, stored relative to the chord between
// the two node centres: U along it, V across it, both in chord lengths, so
// the bend follows the nodes when they move.
type GraphWaypoint struct {
	U float64 `yaml:"u" json:"u"`
	V float64 `yaml:"v" json:"v"`
}

// GraphCanvasEdge holds the custom shape of one directed connection.
type GraphCanvasEdge struct {
	Waypoints []GraphWaypoint `yaml:"waypoints,omitempty" json:"waypoints,omitempty"`
	// LabelT places the latency/rate label along the drawn line (0..1 of
	// its length). Nil means automatic placement.
	LabelT *float64 `yaml:"label_t,omitempty" json:"label_t,omitempty"`
}

// GraphCanvas is one named custom layout.
type GraphCanvas struct {
	ID    string                     `yaml:"id" json:"id"`
	Name  string                     `yaml:"name" json:"name"`
	Nodes map[string]GraphLayoutPos  `yaml:"nodes,omitempty" json:"nodes"`
	Edges map[string]GraphCanvasEdge `yaml:"edges,omitempty" json:"edges"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// ValidateGraphCanvas checks a canvas for storage: id format, name length,
// bounded node/edge counts, at most MaxGraphWaypoints bends per edge and
// finite, bounded numbers everywhere.
func ValidateGraphCanvas(c GraphCanvas) error {
	if !graphCanvasIDRe.MatchString(c.ID) {
		return errors.New("invalid canvas id")
	}
	if !utf8.ValidString(c.Name) || utf8.RuneCountInString(c.Name) > MaxGraphCanvasName {
		return fmt.Errorf("canvas name longer than %d characters", MaxGraphCanvasName)
	}
	if len(c.Nodes) > MaxGraphCanvasNodes {
		return fmt.Errorf("more than %d nodes", MaxGraphCanvasNodes)
	}
	if len(c.Edges) > MaxGraphCanvasEdges {
		return fmt.Errorf("more than %d edges", MaxGraphCanvasEdges)
	}
	for k, p := range c.Nodes {
		if k == "" || len(k) > maxGraphKeyLen {
			return errors.New("invalid node key")
		}
		if !finite(p.X) || !finite(p.Y) || math.Abs(p.X) > maxGraphCoord || math.Abs(p.Y) > maxGraphCoord {
			return fmt.Errorf("invalid position for node %q", k)
		}
	}
	for k, e := range c.Edges {
		if k == "" || len(k) > maxGraphKeyLen*2+8 {
			return errors.New("invalid edge key")
		}
		if len(e.Waypoints) > MaxGraphWaypoints {
			return fmt.Errorf("edge %q has more than %d waypoints", k, MaxGraphWaypoints)
		}
		for _, w := range e.Waypoints {
			if !finite(w.U) || !finite(w.V) || math.Abs(w.U) > maxGraphWaypointRel || math.Abs(w.V) > maxGraphWaypointRel {
				return fmt.Errorf("invalid waypoint on edge %q", k)
			}
		}
		if e.LabelT != nil && (!finite(*e.LabelT) || *e.LabelT < 0 || *e.LabelT > 1) {
			return fmt.Errorf("invalid label position on edge %q", k)
		}
	}
	return nil
}

func cloneGraphCanvas(c GraphCanvas) GraphCanvas {
	out := GraphCanvas{ID: c.ID, Name: c.Name}
	if len(c.Nodes) > 0 {
		out.Nodes = make(map[string]GraphLayoutPos, len(c.Nodes))
		for k, v := range c.Nodes {
			out.Nodes[k] = v
		}
	}
	if len(c.Edges) > 0 {
		out.Edges = make(map[string]GraphCanvasEdge, len(c.Edges))
		for k, v := range c.Edges {
			e := GraphCanvasEdge{}
			if len(v.Waypoints) > 0 {
				e.Waypoints = append([]GraphWaypoint(nil), v.Waypoints...)
			}
			if v.LabelT != nil {
				t := *v.LabelT
				e.LabelT = &t
			}
			out.Edges[k] = e
		}
	}
	return out
}

// GraphCanvases returns a copy of the stored custom canvases, in creation
// order.
func (a *App) GraphCanvases() []GraphCanvas {
	cs := a.store.Get().GraphCanvases
	out := make([]GraphCanvas, 0, len(cs))
	for _, c := range cs {
		out = append(out, cloneGraphCanvas(c))
	}
	return out
}

// ErrGraphCanvasLimit is returned when creating a canvas beyond
// MaxGraphCanvases.
var ErrGraphCanvasLimit = fmt.Errorf("at most %d canvases", MaxGraphCanvases)

// PutGraphCanvas creates or replaces the canvas with c.ID.
func (a *App) PutGraphCanvas(c GraphCanvas) error {
	if err := ValidateGraphCanvas(c); err != nil {
		return err
	}
	c = cloneGraphCanvas(c)
	var err error
	uerr := a.store.Update(func(cfg *Config) {
		next := make([]GraphCanvas, 0, len(cfg.GraphCanvases)+1)
		found := false
		for _, old := range cfg.GraphCanvases {
			if old.ID == c.ID {
				next = append(next, c)
				found = true
			} else {
				next = append(next, old)
			}
		}
		if !found {
			if len(cfg.GraphCanvases) >= MaxGraphCanvases {
				err = ErrGraphCanvasLimit
				return
			}
			next = append(next, c)
		}
		cfg.GraphCanvases = next
	})
	if err != nil {
		return err
	}
	return uerr
}

// DeleteGraphCanvas removes the canvas with the given id. Deleting a missing
// canvas is not an error.
func (a *App) DeleteGraphCanvas(id string) error {
	return a.store.Update(func(cfg *Config) {
		next := make([]GraphCanvas, 0, len(cfg.GraphCanvases))
		for _, c := range cfg.GraphCanvases {
			if c.ID != id {
				next = append(next, c)
			}
		}
		if len(next) == 0 {
			next = nil
		}
		cfg.GraphCanvases = next
	})
}

// MigrateGraphLayout moves the pre-1.4.0 single shared layout
// (graph_layout) into a custom canvas with id DefaultGraphCanvasID, so
// existing node positions survive the upgrade. Runs once at start-up; a
// no-op when there is nothing to migrate.
func MigrateGraphLayout(cfg *Config) bool {
	if len(cfg.GraphLayout) == 0 {
		return false
	}
	exists := false
	for _, c := range cfg.GraphCanvases {
		if c.ID == DefaultGraphCanvasID {
			exists = true
			break
		}
	}
	if !exists && len(cfg.GraphCanvases) < MaxGraphCanvases {
		nodes := make(map[string]GraphLayoutPos, len(cfg.GraphLayout))
		for k, p := range cfg.GraphLayout {
			if k == "" || len(k) > maxGraphKeyLen || !finite(p.X) || !finite(p.Y) ||
				math.Abs(p.X) > maxGraphCoord || math.Abs(p.Y) > maxGraphCoord {
				continue
			}
			if len(nodes) >= MaxGraphCanvasNodes {
				break
			}
			nodes[k] = p
		}
		// Empty name: the UI shows its localised "Default".
		cfg.GraphCanvases = append([]GraphCanvas{{ID: DefaultGraphCanvasID, Nodes: nodes}}, cfg.GraphCanvases...)
	}
	cfg.GraphLayout = nil
	return true
}
