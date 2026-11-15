package vision

import (
	"math"
	"testing"
)

// geomAffineMesh returns a 5 x 4 mesh whose nodes follow the affine map a.
func geomAffineMesh(a func(x, y float64) Point) *Mesh {
	m := &Mesh{Cols: 5, Rows: 4, X0: -3, Y0: 2, StepX: 32, StepY: 30}
	for r := range m.Rows {
		for k := range m.Cols {
			m.Nodes = append(m.Nodes, a(m.X0+float64(k)*m.StepX, m.Y0+float64(r)*m.StepY))
		}
	}
	return m
}

// TestMeshAt: a mesh of an affine map reproduces it everywhere, inside the lattice and beyond it.
func TestMeshAt(t *testing.T) {
	a := func(x, y float64) Point { return Point{3*x - 0.5*y + 100, 0.25*x + 2.5*y - 7} }
	m := geomAffineMesh(a)
	for _, p := range [][2]float64{{0, 0}, {-3, 2}, {125, 92}, {17.3, 41.9}, {-50, -40}, {300, 200}, {64.5, 3}} {
		got, want := m.At(p[0], p[1]), a(p[0], p[1])
		if geomDist(got, want) > 1e-9 {
			t.Errorf("At(%v) = %v, want %v", p, got, want)
		}
	}
}

// TestMeshInvalid: a mesh without enough nodes maps every point to NaN.
func TestMeshInvalid(t *testing.T) {
	for _, m := range []*Mesh{
		{},
		{Cols: 1, Rows: 4, StepX: 1, StepY: 1, Nodes: make([]Point, 4)},
		{Cols: 2, Rows: 2, StepX: 1, StepY: 1, Nodes: make([]Point, 3)},
		{Cols: 2, Rows: 2, StepX: 0, StepY: 1, Nodes: make([]Point, 4)},
		nil,
	} {
		if p := m.At(1, 1); !math.IsNaN(p.X) || !math.IsNaN(p.Y) {
			t.Errorf("%+v: At = %v", m, p)
		}
	}
}
