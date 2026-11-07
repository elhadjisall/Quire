package kv

import (
	"encoding/binary"

	"github.com/elhadjisall/kvdb/btree"
)

// PageInfo describes one page in the file.
type PageInfo struct {
	ID   uint64 `json:"id"`
	Kind string `json:"kind"`
	Keys int    `json:"keys,omitempty"`
	Used int    `json:"used,omitempty"`
}

// PageMap is a classification of every page the master page knows about.
type PageMap struct {
	Pages     []PageInfo `json:"pages"`
	Height    int        `json:"height"`
	LeafKeys  int        `json:"leaf_keys"`
	Internal  int        `json:"internal"`
	Leaves    int        `json:"leaves"`
	FreePages int        `json:"free_pages"`
	FreeItems uint64     `json:"free_items"`
	Unused    int        `json:"unused"`
	Used      uint64     `json:"used"`
}

// PageMap classifies the pages of the last committed version.
func (kv *KV) PageMap() PageMap {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	return kv.pageMap(kv.meta, kv.readPage)
}

func (kv *KV) pageMap(meta master, read func(uint64) []byte) PageMap {
	used := meta.used
	kinds := make([]string, used)
	keys := make([]int, used)
	nbytes := make([]int, used)
	for i := range kinds {
		kinds[i] = "unused"
	}
	if used > 0 {
		kinds[0] = "master"
	}

	seen := map[uint64]bool{}
	for p := meta.headPage; p != 0 && p < used && !seen[p]; {
		seen[p] = true
		if kinds[p] == "unused" {
			kinds[p] = "freelist"
		}
		if p == meta.tailPage {
			break
		}
		page := read(p)
		if len(page) < 8 {
			break
		}
		p = binary.LittleEndian.Uint64(page[:8])
	}

	var stats btree.TreeStats
	if meta.root != 0 {
		tree := btree.BTree{Root: meta.root, Get: read}
		stats = tree.Stats()
		tree.Walk(func(ptr uint64, kind string, nkeys, usedBytes int) {
			if ptr >= used {
				return
			}
			kinds[ptr] = kind
			keys[ptr] = nkeys
			nbytes[ptr] = usedBytes
		})
	}

	pm := PageMap{
		Height:    stats.Height,
		LeafKeys:  stats.Keys,
		Internal:  stats.Internal,
		Leaves:    stats.Leaves,
		FreeItems: meta.tailSeq - meta.headSeq,
		Used:      used,
		Pages:     make([]PageInfo, 0, used),
	}
	for id := uint64(0); id < used; id++ {
		switch kinds[id] {
		case "freelist":
			pm.FreePages++
		case "unused":
			pm.Unused++
		}
		pm.Pages = append(pm.Pages, PageInfo{ID: id, Kind: kinds[id], Keys: keys[id], Used: nbytes[id]})
	}
	return pm
}
