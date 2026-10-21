package graph

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
)

type Kind string

const (
	KindAgent   Kind = "agent"
	KindTool    Kind = "tool"
	KindSkill   Kind = "skill"
	KindToolkit Kind = "toolkit"
)

type Reach struct {
	Agents, Tools, Skills []uuid.UUID
}

type Search struct {
	Vector []float32
	Model  string
	Kinds  []Kind
	Hops   int
	Reach  Reach
	Limit  int
}

type Step struct {
	Kind Kind
	Name string
}

type Hit struct {
	Kind        Kind
	ID          uuid.UUID
	Name        string
	Description string
	Score       float64
	Path        []Step
}

type pattern struct {
	result Kind
	hops   int
	walk   string
	names  []string
	kinds  []Kind
	where  string
}

const (
	allowM = "(g.only IS NULL OR m.name IN g.only)"
	allowR = "(g.only IS NULL OR r.name IN g.only)"
)

var patterns = []pattern{
	{result: KindAgent},
	{result: KindTool},
	{result: KindSkill},
	{
		result: KindAgent, hops: 1, walk: "(r)-[:HAS_SKILL]->(m:Skill)",
		names: []string{"m.name"}, kinds: []Kind{KindSkill},
	},
	{
		result: KindSkill, hops: 1, walk: "(m:Agent)-[:HAS_SKILL]->(r)",
		names: []string{"m.name"}, kinds: []Kind{KindAgent},
	},
	{
		result: KindAgent, hops: 2, walk: "(r)-[g:GRANTED]->(k:Toolkit)-[:CONTAINS]->(m:Tool)",
		names: []string{"k.name", "m.name"}, kinds: []Kind{KindToolkit, KindTool}, where: allowM,
	},
	{
		result: KindSkill, hops: 2, walk: "(m:Skill)<-[:HAS_SKILL]-(a:Agent)-[:HAS_SKILL]->(r)",
		names: []string{"a.name", "m.name"}, kinds: []Kind{KindAgent, KindSkill},
	},
	{
		result: KindTool, hops: 2, walk: "(m:Agent)-[g:GRANTED]->(k:Toolkit)-[:CONTAINS]->(r)",
		names: []string{"k.name", "m.name"}, kinds: []Kind{KindToolkit, KindAgent}, where: allowR,
	},
	{
		result: KindSkill, hops: 3, walk: "(m:Tool)<-[:CONTAINS]-(k:Toolkit)<-[g:GRANTED]-(a:Agent)-[:HAS_SKILL]->(r)",
		names: []string{"a.name", "k.name", "m.name"}, kinds: []Kind{KindAgent, KindToolkit, KindTool}, where: allowM,
	},
	{
		result: KindTool, hops: 3, walk: "(m:Skill)<-[:HAS_SKILL]-(a:Agent)-[g:GRANTED]->(k:Toolkit)-[:CONTAINS]->(r)",
		names: []string{"k.name", "a.name", "m.name"}, kinds: []Kind{KindToolkit, KindAgent, KindSkill}, where: allowR,
	},
}

var labelOf = map[Kind]Label{KindAgent: LabelAgent, KindTool: LabelTool, KindSkill: LabelSkill}

func (r Reach) of(k Kind) []uuid.UUID {
	switch k {
	case KindAgent:
		return r.Agents
	case KindTool:
		return r.Tools
	case KindSkill:
		return r.Skills
	case KindToolkit:
	}
	return nil
}

func (g *Graph) Search(ctx context.Context, team uuid.UUID, s Search) ([]Hit, error) {
	if s.Limit < 1 {
		return nil, fmt.Errorf("graph: a search needs at least one result, and %d were asked for", s.Limit)
	}

	best := map[uuid.UUID]Hit{}
	for _, p := range patterns {
		if p.hops > s.Hops || !slices.Contains(s.Kinds, p.result) {
			continue
		}

		res, err := g.run(ctx, team, true, p.cypher(), map[string]any{
			"reach": idStrings(s.Reach.of(p.result)), "model": s.Model, "q": s.Vector,
		})
		if absent(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}

		for res.Next() {
			h, err := p.hit(res.Record().Values())
			if err != nil {
				return nil, err
			}
			if seen, ok := best[h.ID]; !ok || h.Score > seen.Score {
				best[h.ID] = h
			}
		}
	}

	hits := slices.Collect(maps.Values(best))
	slices.SortFunc(hits, func(a, b Hit) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return hits[:min(len(hits), s.Limit)], nil
}

func (p pattern) cypher() string {
	var b strings.Builder
	fmt.Fprintf(&b, "MATCH (r:%s) WHERE r.id IN $reach\n", labelOf[p.result])
	if p.walk == "" {
		b.WriteString("WITH r, r AS m\n")
	} else {
		fmt.Fprintf(&b, "MATCH %s\n", p.walk)
	}
	b.WriteString("WHERE m.model = $model AND m.embedding IS NOT NULL")
	if p.where != "" {
		b.WriteString(" AND ")
		b.WriteString(p.where)
	}
	fmt.Fprintf(&b, "\nRETURN r.id, r.name, r.description, [%s], 1 - vec.cosineDistance(m.embedding, vecf32($q))",
		strings.Join(p.names, ", "))
	return b.String()
}

func (p pattern) hit(row []any) (Hit, error) {
	rawID, _ := row[0].(string)
	id, err := uuid.Parse(rawID)
	if err != nil {
		return Hit{}, fmt.Errorf("graph: a %s node has the id %q, which is not a uuid", p.result, rawID)
	}

	h := Hit{Kind: p.result, ID: id}
	h.Name, _ = row[1].(string)
	h.Description, _ = row[2].(string)
	h.Path = []Step{{Kind: p.result, Name: h.Name}}

	steps, _ := row[3].([]any)
	if len(steps) != len(p.kinds) {
		return Hit{}, fmt.Errorf("graph: a path of %d steps came back for a walk of %d", len(steps), len(p.kinds))
	}
	for i, s := range steps {
		name, _ := s.(string)
		h.Path = append(h.Path, Step{Kind: p.kinds[i], Name: name})
	}

	switch score := row[4].(type) {
	case float64:
		h.Score = score
	case float32:
		h.Score = float64(score)
	default:
		return Hit{}, fmt.Errorf("graph: a score came back as %T", row[4])
	}
	return h, nil
}

func idStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
