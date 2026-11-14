package telemetry

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// Counter is a monotonic sum per attribute set, cumulative since the process started. It is not safe for
// concurrent use: its owner locks around it.
type Counter struct {
	Name, Unit, Description string
	points                  map[string]*point
}

// Histogram is a distribution per attribute set over fixed bucket bounds, cumulative since the process started. A
// value v is counted in the first bucket whose bound is at least v, or in the last, open bucket. It is not safe for
// concurrent use.
type Histogram struct {
	Name, Unit, Description string
	Bounds                  []float64
	points                  map[string]*point
}

// point is one attribute set's running totals.
type point struct {
	attributes []Attribute
	count      uint64
	sum        float64
	buckets    []uint64
}

// Add counts n more for these attributes.
func (c *Counter) Add(n uint64, attributes ...Attribute) {
	p := pointOf(&c.points, attributes, 0)
	p.count += n
}

// Record counts one value for these attributes.
func (h *Histogram) Record(value float64, attributes ...Attribute) {
	p := pointOf(&h.points, attributes, len(h.Bounds)+1)
	p.count++
	p.sum += value
	bucket, _ := slices.BinarySearch(h.Bounds, value)
	p.buckets[bucket]++
}

// pointOf is the running totals of one attribute set, created on first use.
func pointOf(points *map[string]*point, attributes []Attribute, buckets int) *point {
	if *points == nil {
		*points = map[string]*point{}
	}
	key := attributeKey(attributes)
	p, held := (*points)[key]
	if !held {
		p = &point{attributes: slices.Clone(attributes), buckets: make([]uint64, buckets)}
		(*points)[key] = p
	}
	return p
}

// attributeKey names an attribute set; the order the caller gives them in is part of the name.
func attributeKey(attributes []Attribute) string {
	var key strings.Builder
	for _, attribute := range attributes {
		key.WriteString(strconv.Quote(attribute.Key))
		key.WriteByte('=')
		key.WriteString(strconv.Quote(valueText(attribute.Value)))
		key.WriteByte(';')
	}
	return key.String()
}

// Metrics is one export: every instrument's totals since Start, read at At.
type Metrics struct {
	Start, At  time.Time
	Sums       []Sum
	Histograms []Distribution
}

// Sum is one counter's points; Distribution one histogram's. Their points are copies, in attribute order.
type Sum struct {
	Name, Unit, Description string
	Points                  []SumPoint
}

type SumPoint struct {
	Attributes []Attribute
	Value      uint64
}

type Distribution struct {
	Name, Unit, Description string
	Bounds                  []float64
	Points                  []HistogramPoint
}

type HistogramPoint struct {
	Attributes []Attribute
	Count      uint64
	Sum        float64
	Buckets    []uint64
}

// Sum is the counter's totals now, copied so later Adds do not change them.
func (c *Counter) Sum() Sum {
	sum := Sum{Name: c.Name, Unit: c.Unit, Description: c.Description, Points: []SumPoint{}}
	for _, key := range sortedKeys(c.points) {
		p := c.points[key]
		sum.Points = append(sum.Points, SumPoint{slices.Clone(p.attributes), p.count})
	}
	return sum
}

// Distribution is the histogram's totals now, copied so later Records do not change them.
func (h *Histogram) Distribution() Distribution {
	d := Distribution{Name: h.Name, Unit: h.Unit, Description: h.Description, Bounds: slices.Clone(h.Bounds),
		Points: []HistogramPoint{}}
	for _, key := range sortedKeys(h.points) {
		p := h.points[key]
		d.Points = append(d.Points, HistogramPoint{slices.Clone(p.attributes), p.count, p.sum, slices.Clone(p.buckets)})
	}
	return d
}

func sortedKeys(points map[string]*point) []string {
	keys := make([]string, 0, len(points))
	for key := range points {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
