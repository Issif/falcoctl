// Copyright 2022 The Falco Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package index

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entry describes an entry of the index stored remotely and cached locally.
type Entry struct {
	// Mandatory fields
	Name       string `yaml:"name"`
	Type       string `yaml:"type"`
	Registry   string `yaml:"registry"`
	Repository string `yaml:"repository"`
	// Optional fields
	Description string   `yaml:"description"`
	Home        string   `yaml:"home"`
	Keywords    []string `yaml:"keywords"`
	License     string   `yaml:"license"`
	Maintainers []struct {
		Email string `yaml:"email"`
		Name  string `yaml:"name"`
	} `yaml:"maintainers"`
	Sources []string `yaml:"sources"`
}

// Index represents an index.
type Index struct {
	Name        string
	Entries     []*Entry
	entryByName map[string]*Entry
}

// MergedIndexes is used to aggregate all indexes and perform search operations.
type MergedIndexes struct {
	Index
	indexByEntry map[*Entry]*Index
}

// New returns a new empty Index.
func New(name string) *Index {
	return &Index{
		Name:        name,
		entryByName: map[string]*Entry{},
	}
}

// Upsert adds a new entry to the Index or updates an existing one.
func (i *Index) Upsert(entry *Entry) {
	defer func() {
		i.entryByName[entry.Name] = entry
	}()

	for k, e := range i.Entries {
		if e.Name == entry.Name {
			i.Entries[k] = entry
			return
		}
	}

	i.Entries = append(i.Entries, entry)
}

// Remove removes an entry from the Index.
func (i *Index) Remove(entry *Entry) error {
	for k, e := range i.Entries {
		if e == entry {
			i.Entries = append(i.Entries[:k], i.Entries[k+1:]...)
			delete(i.entryByName, e.Name)
			return nil
		}
	}

	return fmt.Errorf("cannot remove %s: not found", entry.Name)
}

// EntryByName returns a Entry by passing its name.
func (i *Index) EntryByName(name string) (*Entry, bool) {
	entry, ok := i.entryByName[name]
	return entry, ok
}

// Normalize the index to the canonical form (i.e., entries sorted by name,
// lexically byte-wise in ascending order).
//
// Since only one possible representation of a normalized index exists,
// a digest of a normalized index is suitable for integrity checking
// or similar purposes.
// Return an error if the index is not in a consistent state.
func (i *Index) Normalize() error {
	if i == nil {
		return fmt.Errorf("cannot normalize an uninitialized index")
	}

	if len(i.entryByName) != len(i.Entries) {
		return fmt.Errorf("inconsistent index state")
	}

	for _, e := range i.Entries {
		if _, ok := i.entryByName[e.Name]; !ok {
			return fmt.Errorf("inconsistent index state")
		}
	}

	sort.Slice(i.Entries, func(k, j int) bool {
		return i.Entries[k].Name < i.Entries[j].Name
	})

	return nil
}

// Write writes entries to a file.
func (i *Index) Write(path string) error {
	if err := i.Normalize(); err != nil {
		return err
	}
	indexBytes, err := yaml.Marshal(i.Entries)
	if err != nil {
		return fmt.Errorf("cannot marshal index: %w", err)
	}

	if err = os.WriteFile(path, indexBytes, writePermissions); err != nil {
		return fmt.Errorf("cannot write index to file: %w", err)
	}

	return nil
}

// Read reads entries from a file.
func (i *Index) Read(path string) error {
	bytes, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("cannot read index from file: %w", err)
	}

	if err := yaml.Unmarshal(bytes, &i.Entries); err != nil {
		return fmt.Errorf("cannot unmarshal index: %w", err)
	}

	i.entryByName = make(map[string]*Entry, len(i.Entries))
	for _, e := range i.Entries {
		if _, ok := i.entryByName[e.Name]; ok {
			return fmt.Errorf("duplicate entry found: %s", e.Name)
		}
		i.entryByName[e.Name] = e
	}

	return nil
}

// NewMergedIndexes initializes a MergedIndex.
func NewMergedIndexes() *MergedIndexes {
	m := &MergedIndexes{}

	m.entryByName = make(map[string]*Entry)
	m.indexByEntry = make(map[*Entry]*Index)

	return m
}

// Merge creates a new index by merging all the indexes that are passed.
// Orders matters. Be sure to pass an ordered list of indexes. For our use case, sort by added time.
func (m *MergedIndexes) Merge(indexes ...*Index) {
	for _, index := range indexes {
		for _, Entry := range index.Entries {
			m.Upsert(Entry)
			m.indexByEntry[Entry] = index
		}
	}
}

// SearchByKeywords search for entries matching the given keywords in MergedIndexes.
// minScore is the minimum score to consider a match between a name of an artifact and a keyword.
// if minScore is not reached, we fallback to a simple partial matching on keywords.
func (i *Index) SearchByKeywords(minScore float64, keywords ...string) []*Entry {
	matches := make(map[*Entry]struct{})

	for _, entry := range i.Entries {
		entryKeywords := strings.Join(entry.Keywords, " ")

		for _, keyword := range keywords {
			// Compute score between the keyword and entry name.
			score := score(entry.Name, keyword)

			if score >= minScore || strings.Contains(entryKeywords, keyword) {
				matches[entry] = struct{}{}
				break
			}
		}
	}

	result := make([]*Entry, 0, len(matches))
	for k := range matches {
		result = append(result, k)
	}

	return result
}

// IndexByEntry is used to retrieve the original index from an entry in MergedIndexes.
func (m *MergedIndexes) IndexByEntry(entry *Entry) *Index {
	return m.indexByEntry[entry]
}

// levenshteinDistance computes the edit distance between two strings.
func levenshteinDistance(s, t string) int {
	s = strings.ToLower(s)
	t = strings.ToLower(t)

	d := make([][]int, len(s)+1)

	for i := range d {
		d[i] = make([]int, len(t)+1)
	}

	for i := range d {
		d[i][0] = i
	}

	for j := range d[0] {
		d[0][j] = j
	}

	for j := 1; j <= len(t); j++ {
		for i := 1; i <= len(s); i++ {
			if s[i-1] == t[j-1] {
				d[i][j] = d[i-1][j-1]
			} else {
				min := d[i-1][j]
				if d[i][j-1] < min {
					min = d[i][j-1]
				}
				if d[i-1][j-1] < min {
					min = d[i-1][j-1]
				}
				d[i][j] = min + 1
			}
		}
	}

	return d[len(s)][len(t)]
}

func score(s, t string) float64 {
	distance := levenshteinDistance(s, t)

	longerLen := math.Max(float64(len(s)), float64(len(t)))

	// The maximum levenshtein distance between two strings is equal
	// to the length of the longer string. We can use this to compute
	// a ratio.
	return (longerLen - float64(distance)) / longerLen
}
