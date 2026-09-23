package ctr

import (
	_ "embed"
	"fmt"
	"strconv"
	"strings"
)

//go:embed data/skus.tsv
var skusTSV string

//go:embed data/race-rewards.tsv
var raceTSV string

// sku is a Pit Stop product: the catalog items it grants, and its price. A bundle lists its
// parts and costs the sum of the parts the player does not own yet.
type sku struct {
	price int64
	items []uint32
	parts []uint32
}

var catalog = mustCatalog()

// raceRewards maps the game's track hash to the coins for 1st, 2nd, 3rd, and 4th or below.
var raceRewards = mustRaceRewards()

func rows(tsv string, fields int) [][]string {
	var out [][]string
	for _, line := range strings.Split(tsv, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < fields {
			panic(fmt.Sprintf("ctr data: short line %q", line))
		}
		out = append(out, f)
	}
	return out
}

func ids(s string) []uint32 {
	if s == "-" {
		return nil
	}
	var out []uint32
	for _, p := range strings.Split(s, ",") {
		v, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			panic(fmt.Sprintf("ctr data: id %q", p))
		}
		out = append(out, uint32(v))
	}
	return out
}

func mustCatalog() map[uint32]sku {
	m := map[uint32]sku{}
	for _, f := range rows(skusTSV, 4) {
		id, err1 := strconv.ParseUint(f[0], 10, 32)
		price, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 != nil || err2 != nil || price < 0 {
			panic(fmt.Sprintf("ctr data: sku line %q", f))
		}
		m[uint32(id)] = sku{price: price, items: ids(f[2]), parts: ids(f[3])}
	}
	return m
}

func mustRaceRewards() map[uint64][4]int64 {
	m := map[uint64][4]int64{}
	for _, f := range rows(raceTSV, 6) {
		h, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			panic(fmt.Sprintf("ctr data: race line %q", f))
		}
		var coins [4]int64
		for i := range coins {
			if coins[i], err = strconv.ParseInt(f[2+i], 10, 64); err != nil {
				panic(fmt.Sprintf("ctr data: race line %q", f))
			}
		}
		m[h] = coins
	}
	return m
}
