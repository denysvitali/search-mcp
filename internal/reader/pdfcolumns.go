package reader

import (
	"math"
	"strings"

	"github.com/ledongthuc/pdf"
)

// orderPDFRows recognizes a repeated central gutter in prose-heavy two-column
// papers. Short table cells and sparse pages retain their positional row order.
// Full-width headings delimit blocks, whose columns are read left then right.
func orderPDFRows(rows [][]pdf.Text) [][]pdf.Text {
	if len(rows) < 8 {
		return rows
	}
	minX, maxX := 1e9, -1e9
	for _, row := range rows {
		for _, t := range row {
			if strings.TrimSpace(t.S) == "" {
				continue
			}
			minX = math.Min(minX, t.X)
			maxX = math.Max(maxX, t.X+t.W)
		}
	}
	if maxX-minX < 200 {
		return rows
	}
	split := (minX + maxX) / 2
	paired := 0
	spanning := 0
	for _, row := range rows {
		left, right, crosses := splitPDFRow(row, split)
		if crosses {
			spanning++
			continue
		}
		if len(strings.TrimSpace(joinPDFRow(left))) >= 25 && len(strings.TrimSpace(joinPDFRow(right))) >= 25 {
			paired++
		}
	}
	if paired < 5 || paired*3 < len(rows) || spanning*4 > len(rows) {
		return rows
	}
	ordered := make([][]pdf.Text, 0, len(rows)*2)
	var leftRows, rightRows [][]pdf.Text
	flush := func() {
		ordered = append(ordered, leftRows...)
		ordered = append(ordered, rightRows...)
		leftRows, rightRows = nil, nil
	}
	for _, row := range rows {
		left, right, crosses := splitPDFRow(row, split)
		if crosses {
			flush()
			ordered = append(ordered, row)
			continue
		}
		if len(left) > 0 {
			leftRows = append(leftRows, left)
		}
		if len(right) > 0 {
			rightRows = append(rightRows, right)
		}
	}
	flush()
	return ordered
}

func splitPDFRow(row []pdf.Text, split float64) (left, right []pdf.Text, crosses bool) {
	for _, t := range row {
		if strings.TrimSpace(t.S) != "" && t.X < split+6 && t.X+t.W > split-6 {
			crosses = true
		}
		if t.X < split {
			left = append(left, t)
		} else {
			right = append(right, t)
		}
	}
	return
}
