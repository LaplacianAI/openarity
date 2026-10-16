package skill

import (
	"fmt"
	"io"
)

type Collector struct {
	entries []Entry
	total   int
}

func (c *Collector) Add(path string, r io.Reader) error {
	if len(c.entries) == MaxFiles {
		return fmt.Errorf("a skill holds at most %d files", MaxFiles)
	}

	budget := min(MaxFileBytes, MaxSkillBytes-c.total)
	b, err := io.ReadAll(io.LimitReader(r, int64(budget)+1))
	if err != nil {
		return fmt.Errorf("%q cannot be read: %w", path, err)
	}
	if len(b) > MaxFileBytes {
		return fmt.Errorf("%q is over %d MiB", path, MaxFileBytes>>20)
	}
	c.total += len(b)
	if c.total > MaxSkillBytes {
		return fmt.Errorf("a skill is at most %d MiB", MaxSkillBytes>>20)
	}

	c.entries = append(c.entries, Entry{Path: path, Data: b})
	return nil
}

func (c *Collector) Entries() []Entry {
	return c.entries
}
