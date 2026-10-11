package graph

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	falkordb "github.com/FalkorDB/falkordb-go/v2"
	"github.com/google/uuid"
)

type Label string

const (
	LabelTeam    Label = "Team"
	LabelAgent   Label = "Agent"
	LabelToolkit Label = "Toolkit"
	LabelTool    Label = "Tool"
	LabelSkill   Label = "Skill"
)

type EdgeType string

const (
	EdgeOwns     EdgeType = "OWNS"
	EdgeGranted  EdgeType = "GRANTED"
	EdgeContains EdgeType = "CONTAINS"
	EdgeHasSkill EdgeType = "HAS_SKILL"
)

const (
	readTimeout  = 10 * time.Second
	pageSize     = 1000
	maxErrorText = 300

	msgAlreadyIndexed = "already indexed"
	msgNoSuchGraph    = "Invalid graph operation on empty key"
)

var (
	allLabels      = []Label{LabelTeam, LabelAgent, LabelToolkit, LabelTool, LabelSkill}
	embeddedLabels = []Label{LabelAgent, LabelTool, LabelSkill}
	reserved       = []string{"id", "embedding", "model", "content_sha256"}
)

type Ref struct {
	Label Label
	ID    uuid.UUID
}

type Node struct {
	Ref
	Props         map[string]any
	Embedding     []float32
	Model         string
	ContentSHA256 []byte
}

type Edge struct {
	To    uuid.UUID
	Props map[string]any
}

type StoredEdge struct {
	Type  EdgeType
	From  Ref
	To    Ref
	Props map[string]any
}

type Snapshot struct {
	Nodes []Node
	Edges []StoredEdge
}

type Graph struct {
	db   *falkordb.FalkorDB
	dims int

	mu    sync.Mutex
	turns map[uuid.UUID]chan struct{}
}

type outcome[T any] struct {
	value T
	err   error
}

func Dial(rawURL string, dims int) (*Graph, error) {
	if dims < 1 {
		return nil, fmt.Errorf("graph: a vector index needs at least 1 dimension, and EMBEDDING_DIMENSIONS is %d", dims)
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("graph: FALKOR_DB_URL is not a URL")
	}
	q := u.Query()
	q.Set("read_timeout", readTimeout.String())
	q.Set("max_retries", "-1")
	u.RawQuery = q.Encode()

	db, err := falkordb.FromURL(u.String())
	if err != nil {
		return nil, fmt.Errorf("graph: %w", err)
	}

	return &Graph{db: db, dims: dims, turns: map[uuid.UUID]chan struct{}{}}, nil
}

func (g *Graph) Ping(ctx context.Context) error {
	_, err := ordered(ctx, make(chan struct{}, 1), func() (struct{}, error) {
		return struct{}{}, g.db.Conn.Ping(ctx).Err()
	})
	return brief(err)
}

func (g *Graph) Close() error { return g.db.Conn.Close() }

func graphName(team uuid.UUID) string { return "team:" + team.String() }

func (g *Graph) EnsureIndexes(ctx context.Context, team uuid.UUID) error {
	var statements []string
	for _, l := range allLabels {
		statements = append(statements, fmt.Sprintf("CREATE INDEX FOR (n:%s) ON (n.id)", l))
	}
	for _, l := range embeddedLabels {
		statements = append(statements,
			fmt.Sprintf("CREATE VECTOR INDEX FOR (n:%s) ON (n.embedding) OPTIONS {dimension:%d, similarityFunction:'cosine'}", l, g.dims),
			fmt.Sprintf("CREATE FULLTEXT INDEX FOR (n:%s) ON (n.name, n.description)", l))
	}

	for _, statement := range statements {
		_, err := g.run(ctx, team, false, statement, nil)
		if err != nil && !strings.Contains(err.Error(), msgAlreadyIndexed) {
			return err
		}
	}

	return g.verifyIndexes(ctx, team)
}

func (g *Graph) PutNode(ctx context.Context, team uuid.UUID, n Node) error {
	if err := named(string(n.Label)); err != nil {
		return err
	}
	for _, key := range reserved {
		if _, clash := n.Props[key]; clash {
			return fmt.Errorf("graph: the property %s is set by the graph, not by its caller", key)
		}
	}

	cypher := fmt.Sprintf("MERGE (n:%s {id: $id}) SET n += $props", n.Label)
	params := map[string]any{"id": n.ID.String(), "props": n.Props}

	if n.Embedding != nil {
		switch {
		case !slices.Contains(embeddedLabels, n.Label):
			return fmt.Errorf("graph: a %s has no vector index, so its vector could never be searched", n.Label)
		case len(n.Embedding) != g.dims:
			return fmt.Errorf("graph: a vector of %d dimensions for an index of %d", len(n.Embedding), g.dims)
		}
		cypher += ", n.embedding = vecf32($embedding), n.model = $model, n.content_sha256 = $sha"
		params["embedding"] = n.Embedding
		params["model"] = n.Model
		params["sha"] = hex.EncodeToString(n.ContentSHA256)
	}

	_, err := g.run(ctx, team, false, cypher, params)
	return err
}

func (g *Graph) Fingerprint(ctx context.Context, team uuid.UUID, ref Ref) (model string, sha []byte, found bool, err error) {
	if err := named(string(ref.Label)); err != nil {
		return "", nil, false, err
	}

	cypher := fmt.Sprintf("MATCH (n:%s {id: $id}) RETURN n.model, n.content_sha256", ref.Label)
	res, err := g.run(ctx, team, true, cypher, map[string]any{"id": ref.ID.String()})
	if absent(err) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	if !res.Next() {
		return "", nil, false, nil
	}

	row := res.Record().Values()
	model, _ = row[0].(string)
	hexed, _ := row[1].(string)
	sha, err = hex.DecodeString(hexed)
	if err != nil {
		return "", nil, false, fmt.Errorf("graph: %s %s has a content hash that is not hex", ref.Label, ref.ID)
	}
	return model, sha, true, nil
}

func (g *Graph) SetOutEdges(ctx context.Context, team uuid.UUID, from Ref, typ EdgeType, to Label, edges []Edge) error {
	for _, name := range []string{string(from.Label), string(typ), string(to)} {
		if err := named(name); err != nil {
			return err
		}
	}

	rows := make([]map[string]any, len(edges))
	seen := map[uuid.UUID]bool{}
	for i, e := range edges {
		if seen[e.To] {
			return fmt.Errorf("graph: %s is the target of two %s edges", e.To, typ)
		}
		seen[e.To] = true
		rows[i] = map[string]any{"to": e.To.String(), "props": e.Props}
	}

	cypher := fmt.Sprintf("MATCH (f:%[1]s {id: $from})\n"+
		"OPTIONAL MATCH (f)-[stale:%[2]s]->(:%[3]s) DELETE stale\n"+
		"WITH DISTINCT f UNWIND $edges AS e\n"+
		"MATCH (t:%[3]s {id: e.to})\n"+
		"CREATE (f)-[fresh:%[2]s]->(t) SET fresh += e.props\n"+
		"RETURN count(t)", from.Label, typ, to)

	res, err := g.run(ctx, team, false, cypher, map[string]any{"from": from.ID.String(), "edges": rows})
	if err != nil {
		return err
	}

	var written int64
	if res.Next() {
		written, _ = res.Record().Values()[0].(int64)
	}
	if int(written) != len(edges) {
		return fmt.Errorf("graph: %d of %d %s edges were written; the %s %s or a %s it points at is not in the graph yet",
			written, len(edges), typ, from.Label, from.ID, to)
	}
	return nil
}

func (g *Graph) DeleteNode(ctx context.Context, team uuid.UUID, ref Ref) error {
	if err := named(string(ref.Label)); err != nil {
		return err
	}

	cypher := fmt.Sprintf("MATCH (n:%s {id: $id}) DETACH DELETE n", ref.Label)
	_, err := g.run(ctx, team, false, cypher, map[string]any{"id": ref.ID.String()})
	return err
}

func (g *Graph) DropTeam(ctx context.Context, team uuid.UUID) error {
	_, err := ordered(ctx, g.turn(team), func() (struct{}, error) {
		return struct{}{}, g.db.SelectGraph(graphName(team)).Delete()
	})
	if absent(err) {
		return nil
	}
	return brief(err)
}

func (g *Graph) Snapshot(ctx context.Context, team uuid.UUID) (Snapshot, error) {
	var out Snapshot

	err := g.pages(ctx, team,
		"MATCH (n) RETURN labels(n)[0], n.id, properties(n) ORDER BY labels(n)[0], n.id",
		func(row []any) error {
			n, err := nodeOf(row)
			out.Nodes = append(out.Nodes, n)
			return err
		})
	if err != nil {
		return Snapshot{}, err
	}

	err = g.pages(ctx, team,
		"MATCH (a)-[r]->(b) RETURN type(r), labels(a)[0], a.id, labels(b)[0], b.id, properties(r) "+
			"ORDER BY type(r), a.id, b.id",
		func(row []any) error {
			e, err := edgeOf(row)
			out.Edges = append(out.Edges, e)
			return err
		})
	if err != nil {
		return Snapshot{}, err
	}

	return out, nil
}

func (g *Graph) verifyIndexes(ctx context.Context, team uuid.UUID) error {
	res, err := g.run(ctx, team, true, "CALL db.indexes() YIELD label, options RETURN label, options", nil)
	if err != nil {
		return err
	}

	widths := map[Label]int64{}
	for res.Next() {
		row := res.Record().Values()
		label, _ := row[0].(string)
		options, _ := row[1].(map[string]any)
		vector, _ := options["embedding"].(map[string]any)
		if width, ok := vector["dimension"].(int64); ok {
			widths[Label(label)] = width
		}
	}

	for _, l := range embeddedLabels {
		width, ok := widths[l]
		switch {
		case !ok:
			return fmt.Errorf("graph: the %s vector index is missing, or its width cannot be read, after it was created", l)
		case width != int64(g.dims):
			return fmt.Errorf("graph: the %s index holds vectors of %d dimensions and EMBEDDING_DIMENSIONS is %d; "+
				"the model changed, so run `brain graph rebuild`", l, width, g.dims)
		}
	}
	return nil
}

func (g *Graph) pages(ctx context.Context, team uuid.UUID, cypher string, each func(row []any) error) error {
	for skip := 0; ; skip += pageSize {
		res, err := g.run(ctx, team, true, fmt.Sprintf("%s SKIP %d LIMIT %d", cypher, skip, pageSize), nil)
		if absent(err) {
			return nil
		}
		if err != nil {
			return err
		}

		rows := 0
		for res.Next() {
			rows++
			if err := each(res.Record().Values()); err != nil {
				return err
			}
		}
		if rows < pageSize {
			return nil
		}
	}
}

func (g *Graph) run(ctx context.Context, team uuid.UUID, readOnly bool, cypher string, params map[string]any) (*falkordb.QueryResult, error) {
	head, err := header(params)
	if err != nil {
		return nil, err
	}

	graph := g.db.SelectGraph(graphName(team))
	res, err := ordered(ctx, g.turn(team), func() (*falkordb.QueryResult, error) {
		if readOnly {
			return graph.ROQuery(head+cypher, nil, nil)
		}
		return graph.Query(head+cypher, nil, nil)
	})
	if err != nil {
		return nil, brief(err)
	}
	return res, nil
}

func (g *Graph) turn(team uuid.UUID) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.turns[team] == nil {
		g.turns[team] = make(chan struct{}, 1)
	}
	return g.turns[team]
}

func ordered[T any](ctx context.Context, turn chan struct{}, fn func() (T, error)) (T, error) {
	var zero T

	select {
	case turn <- struct{}{}:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-turn
		return zero, err
	}

	done := make(chan outcome[T], 1)
	go func() {
		defer func() { <-turn }()
		value, err := fn()
		done <- outcome[T]{value: value, err: err}
	}()

	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case o := <-done:
		return o.value, o.err
	}
}

func brief(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	msg := err.Error()
	if len(msg) > maxErrorText {
		msg = strings.ToValidUTF8(msg[:maxErrorText], "") + "…"
	}
	return errors.New("graph: " + msg)
}

func absent(err error) bool {
	return err != nil && strings.Contains(err.Error(), msgNoSuchGraph)
}

func named(name string) error {
	if !identifier.MatchString(name) {
		return fmt.Errorf("graph: %q is not a label or an edge type", name)
	}
	return nil
}

func nodeOf(row []any) (Node, error) {
	label, _ := row[0].(string)
	rawID, _ := row[1].(string)
	props, _ := row[2].(map[string]any)

	id, err := uuid.Parse(rawID)
	if err != nil {
		return Node{}, fmt.Errorf("graph: a %s node has the id %q, which is not a uuid", label, rawID)
	}

	n := Node{Ref: Ref{Label: Label(label), ID: id}}
	n.Embedding, _ = props["embedding"].([]float32)
	n.Model, _ = props["model"].(string)
	if hexed, _ := props["content_sha256"].(string); hexed != "" {
		if n.ContentSHA256, err = hex.DecodeString(hexed); err != nil {
			return Node{}, fmt.Errorf("graph: %s %s has a content hash that is not hex", label, id)
		}
	}

	for _, key := range reserved {
		delete(props, key)
	}
	n.Props = props
	return n, nil
}

func edgeOf(row []any) (StoredEdge, error) {
	typ, _ := row[0].(string)
	fromLabel, _ := row[1].(string)
	fromID, _ := row[2].(string)
	toLabel, _ := row[3].(string)
	toID, _ := row[4].(string)
	props, _ := row[5].(map[string]any)

	from, err := uuid.Parse(fromID)
	if err != nil {
		return StoredEdge{}, fmt.Errorf("graph: a %s edge starts at the id %q, which is not a uuid", typ, fromID)
	}
	to, err := uuid.Parse(toID)
	if err != nil {
		return StoredEdge{}, fmt.Errorf("graph: a %s edge ends at the id %q, which is not a uuid", typ, toID)
	}

	return StoredEdge{
		Type:  EdgeType(typ),
		From:  Ref{Label: Label(fromLabel), ID: from},
		To:    Ref{Label: Label(toLabel), ID: to},
		Props: props,
	}, nil
}
