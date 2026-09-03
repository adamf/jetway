package inventory

import (
	"fmt"
	"math"
	"sort"
)

// The network linear programme. A leg's bid price from its own ladder
// (network.go) knows nothing of the itineraries that share the leg; the
// deterministic linear programme does: allocate seats to itineraries by
// fare within every leg's capacity and every itinerary's forecast demand,
// and the shadow price of each leg's capacity is its bid price -- what one
// more seat on that leg would earn the network. This is the DLP of the
// revenue management textbooks (Talluri and van Ryzin, chapter 3), solved
// here by a plain dense simplex: the problems a carrier's day poses are
// hundreds of legs and itineraries, not millions.

// Itinerary is one product the network sells: the legs it uses, its fare
// and its forecast demand.
type Itinerary struct {
	Legs   []string
	Fare   float64
	Demand float64
}

// Simplex maximises c·x subject to A x ≤ b and x ≥ 0, with b ≥ 0 so the
// origin is feasible, and returns the optimum x, the constraint duals y
// (one per row of A) and the objective. Bland's rule picks the pivots, so
// it terminates.
func Simplex(c []float64, A [][]float64, b []float64) (x, y []float64, obj float64, err error) {
	m, n := len(A), len(c)
	if m != len(b) {
		return nil, nil, 0, fmt.Errorf("lp: %d rows but %d bounds", m, len(b))
	}
	for i, row := range A {
		if len(row) != n {
			return nil, nil, 0, fmt.Errorf("lp: row %d has %d columns, want %d", i, len(row), n)
		}
		if b[i] < 0 {
			return nil, nil, 0, fmt.Errorf("lp: row %d has a negative bound; the origin must be feasible", i)
		}
	}
	// Tableau: m constraint rows then the objective row; n variables, m
	// slacks, the right-hand side.
	w := n + m + 1
	t := make([][]float64, m+1)
	for i := 0; i < m; i++ {
		t[i] = make([]float64, w)
		copy(t[i], A[i])
		t[i][n+i] = 1
		t[i][w-1] = b[i]
	}
	t[m] = make([]float64, w)
	for j := 0; j < n; j++ {
		t[m][j] = -c[j]
	}
	basis := make([]int, m)
	for i := range basis {
		basis[i] = n + i
	}
	const eps = 1e-9
	for iter := 0; iter < 50*(m+n)+100; iter++ {
		// Entering: the lowest-indexed column with a negative objective coefficient.
		enter := -1
		for j := 0; j < w-1; j++ {
			if t[m][j] < -eps {
				enter = j
				break
			}
		}
		if enter < 0 {
			break
		}
		// Leaving: the tightest ratio, lowest basis index on ties.
		leave := -1
		best := math.Inf(1)
		for i := 0; i < m; i++ {
			if t[i][enter] > eps {
				r := t[i][w-1] / t[i][enter]
				if r < best-eps || (math.Abs(r-best) <= eps && leave >= 0 && basis[i] < basis[leave]) {
					best, leave = r, i
				}
			}
		}
		if leave < 0 {
			return nil, nil, 0, fmt.Errorf("lp: unbounded in column %d", enter)
		}
		p := t[leave][enter]
		for j := range t[leave] {
			t[leave][j] /= p
		}
		for i := range t {
			if i == leave || math.Abs(t[i][enter]) <= eps {
				continue
			}
			f := t[i][enter]
			for j := range t[i] {
				t[i][j] -= f * t[leave][j]
			}
		}
		basis[leave] = enter
	}
	x = make([]float64, n)
	for i, bj := range basis {
		if bj < n {
			x[bj] = t[i][w-1]
		}
	}
	y = make([]float64, m)
	for i := 0; i < m; i++ {
		y[i] = math.Max(0, t[m][n+i])
	}
	return x, y, t[m][w-1], nil
}

// NetworkBidPrices solves the network's deterministic programme: seats go
// to itineraries by fare within each leg's remaining capacity and each
// itinerary's demand still to come, and each leg's bid price is the dual
// of its capacity constraint. Legs no itinerary uses price at zero; a leg
// with no capacity left prices at the highest fare over it. The
// allocation comes back alongside, itinerary by itinerary.
func NetworkBidPrices(capacity map[string]float64, its []Itinerary) (map[string]float64, []float64, error) {
	legs := make([]string, 0, len(capacity))
	for l := range capacity {
		legs = append(legs, l)
	}
	sort.Strings(legs)
	index := map[string]int{}
	for i, l := range legs {
		index[l] = i
	}
	var A [][]float64
	var b []float64
	for _, l := range legs {
		row := make([]float64, len(its))
		for j, it := range its {
			for _, use := range it.Legs {
				if use == l {
					row[j] = 1
				}
			}
		}
		A = append(A, row)
		b = append(b, math.Max(0, capacity[l]))
	}
	c := make([]float64, len(its))
	for j, it := range its {
		row := make([]float64, len(its))
		row[j] = 1
		A = append(A, row)
		b = append(b, math.Max(0, it.Demand))
		c[j] = it.Fare
	}
	x, y, _, err := Simplex(c, A, b)
	if err != nil {
		return nil, nil, err
	}
	bids := make(map[string]float64, len(legs))
	for i, l := range legs {
		bids[l] = y[i]
	}
	return bids, x, nil
}
