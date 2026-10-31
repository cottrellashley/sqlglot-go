package sqlglot

// Port of sqlglot/diff.py: semantic diffing of expression trees (the Change Distiller
// algorithm).

import (
	"reflect"
	"sort"

	"github.com/cottrellashley/sqlglot-go/exp"
)

// Edit is one step of an edit script: an Insert, Remove, Move, Update or Keep.
type Edit interface{ isEdit() }

// Insert indicates that a new node has been inserted.
type Insert struct{ Expression *exp.Expr }

// Remove indicates that an existing node has been removed.
type Remove struct{ Expression *exp.Expr }

// Move indicates that an existing node's position within the tree has changed.
type Move struct{ Source, Target *exp.Expr }

// Update indicates that an existing node has been updated.
type Update struct{ Source, Target *exp.Expr }

// Keep indicates that an existing node hasn't been changed.
type Keep struct{ Source, Target *exp.Expr }

func (Insert) isEdit() {}
func (Remove) isEdit() {}
func (Move) isEdit()   {}
func (Update) isEdit() {}
func (Keep) isEdit()   {}

// DiffOptions are the optional arguments of Diff.
type DiffOptions struct {
	// Matchings are pre-matched (source, target) node pairs, which help the heuristics for
	// subtrees known to be matching. The nodes must belong to the source and target trees.
	Matchings [][2]*exp.Expr
	// DeltaOnly excludes the Keep edits.
	DeltaOnly bool
	// F and T are the ChangeDistiller thresholds (0 means 0.6).
	F, T float64
	// Dialect is used to generate the SQL compared by the similarity heuristics.
	Dialect any
}

// Diff mirrors diff.diff: it returns the edits that transform the source expression tree into
// the target one, for every node of both trees.
func Diff(source, target *exp.Expr, o DiffOptions) (edits []Edit, err error) {
	defer recoverOptimize(&err)

	// if the source and target share nodes, the parent / hierarchies would be inaccurate, so
	// the trees are copied
	var sourceNodes, targetNodes []*exp.Expr
	for n := range source.Walk(true, nil) {
		sourceNodes = append(sourceNodes, n)
	}
	for n := range target.Walk(true, nil) {
		targetNodes = append(targetNodes, n)
	}
	sourceIDs := map[*exp.Expr]bool{}
	for _, n := range sourceNodes {
		sourceIDs[n] = true
	}
	targetIDs := map[*exp.Expr]bool{}
	shared := false
	for _, n := range targetNodes {
		targetIDs[n] = true
		if sourceIDs[n] {
			shared = true
		}
	}
	copy := len(sourceNodes) != len(sourceIDs) || len(targetNodes) != len(targetIDs) || shared

	matchings := o.Matchings
	if copy {
		sourceCopy, targetCopy := source.Copy(), target.Copy()
		if len(matchings) > 0 {
			sourceMapping := nodeMapping(sourceNodes, sourceCopy)
			targetMapping := nodeMapping(targetNodes, targetCopy)
			mapped := make([][2]*exp.Expr, len(matchings))
			for i, m := range matchings {
				mapped[i] = [2]*exp.Expr{sourceMapping[m[0]], targetMapping[m[1]]}
			}
			matchings = mapped
		}
		source, target = sourceCopy, targetCopy
	}

	d, err := GetDialect(o.Dialect)
	if err != nil {
		return nil, err
	}
	cd := &changeDistiller{f: o.F, t: o.T, dialect: d}
	if cd.f == 0 {
		cd.f = 0.6
	}
	if cd.t == 0 {
		cd.t = 0.6
	}
	return cd.diff(source, target, matchings, o.DeltaOnly), nil
}

func nodeMapping(oldNodes []*exp.Expr, newRoot *exp.Expr) map[*exp.Expr]*exp.Expr {
	var newNodes []*exp.Expr
	for n := range newRoot.Walk(true, nil) {
		newNodes = append(newNodes, n)
	}
	m := map[*exp.Expr]*exp.Expr{}
	for i := range min(len(oldNodes), len(newNodes)) {
		m[oldNodes[len(oldNodes)-1-i]] = newNodes[len(newNodes)-1-i]
	}
	return m
}

// updatableKinds are the expression kinds for which Update edits are allowed.
var updatableKinds = []exp.Kind{exp.Alias, exp.Boolean, exp.Column, exp.DataType, exp.Lambda, exp.Literal, exp.Table, exp.Window}

func isIgnoredLeaf(e *exp.Expr) bool { return e.Is(exp.Identifier) }

// changeDistiller implements the Change Distiller algorithm described by Beat Fluri and Martin
// Pinzger (https://ieeexplore.ieee.org/document/4339230), based on the algorithm of Chawathe
// et al. (http://ilpubs.stanford.edu:8090/115/1/1995-46.pdf).
type changeDistiller struct {
	f, t    float64
	dialect *Dialect

	source, target   *exp.Expr
	sourceOrder      []*exp.Expr // bfs order, ignored leaves excluded
	targetOrder      []*exp.Expr
	unmatchedSource  map[*exp.Expr]bool
	unmatchedTarget  map[*exp.Expr]bool
	bigramHistoCache map[*exp.Expr]map[string]int
	sourceIndex      map[*exp.Expr]bool
	targetIndex      map[*exp.Expr]bool
}

func bfsIndex(root *exp.Expr) ([]*exp.Expr, map[*exp.Expr]bool) {
	var order []*exp.Expr
	index := map[*exp.Expr]bool{}
	for n := range root.BFS(nil) {
		if !isIgnoredLeaf(n) && !index[n] {
			index[n] = true
			order = append(order, n)
		}
	}
	return order, index
}

func (c *changeDistiller) diff(source, target *exp.Expr, matchings [][2]*exp.Expr, deltaOnly bool) []Edit {
	preMatched := map[*exp.Expr]*exp.Expr{}
	preMatchedTargets := map[*exp.Expr]bool{}
	for _, m := range matchings {
		preMatched[m[0]] = m[1]
		preMatchedTargets[m[1]] = true
	}
	c.source, c.target = source, target
	c.sourceOrder, c.sourceIndex = bfsIndex(source)
	c.targetOrder, c.targetIndex = bfsIndex(target)
	c.unmatchedSource = map[*exp.Expr]bool{}
	for _, n := range c.sourceOrder {
		if _, ok := preMatched[n]; !ok {
			c.unmatchedSource[n] = true
		}
	}
	c.unmatchedTarget = map[*exp.Expr]bool{}
	for _, n := range c.targetOrder {
		if !preMatchedTargets[n] {
			c.unmatchedTarget[n] = true
		}
	}
	c.bigramHistoCache = map[*exp.Expr]map[string]int{}

	matchingSet := c.computeMatchingSet()
	for s, t := range preMatched {
		matchingSet[s] = t
	}
	return c.generateEditScript(matchingSet, deltaOnly)
}

func (c *changeDistiller) generateEditScript(matchings map[*exp.Expr]*exp.Expr, deltaOnly bool) []Edit {
	var script []Edit
	// iterate the bfs-ordered indexes so the edit script has a stable, tree-order sequence
	for _, n := range c.sourceOrder {
		if c.unmatchedSource[n] {
			script = append(script, Remove{n})
		}
	}
	for _, n := range c.targetOrder {
		if c.unmatchedTarget[n] {
			script = append(script, Insert{n})
		}
	}
	for _, sourceNode := range c.sourceOrder {
		targetNode, ok := matchings[sourceNode]
		if !ok {
			continue
		}
		identical := sourceNode.Equal(targetNode)
		if !sourceNode.IsAny(updatableKinds...) || identical {
			if identical {
				sp, tp := sourceNode.Parent(), targetNode.Parent()
				if (sp != nil && tp == nil) || (sp == nil && tp != nil) || (sp != nil && tp != nil && matchings[sp] != tp) {
					script = append(script, Move{sourceNode, targetNode})
				}
			} else {
				script = append(script, c.generateMoveEdits(sourceNode, targetNode, matchings)...)
			}
			if !reflect.DeepEqual(nonExpressionLeaves(sourceNode), nonExpressionLeaves(targetNode)) {
				script = append(script, Update{sourceNode, targetNode})
			} else if !deltaOnly {
				script = append(script, Keep{sourceNode, targetNode})
			}
		} else {
			script = append(script, Update{sourceNode, targetNode})
		}
	}
	return script
}

func (c *changeDistiller) generateMoveEdits(source, target *exp.Expr, matchings map[*exp.Expr]*exp.Expr) []Edit {
	sourceArgs := expressionOnlyArgs(source)
	targetArgs := expressionOnlyArgs(target)
	inLCS := map[*exp.Expr]bool{}
	for _, a := range lcs(sourceArgs, targetArgs, func(l, r *exp.Expr) bool { return matchings[l] == r && r != nil }) {
		inLCS[a] = true
	}
	var moves []Edit
	for _, a := range sourceArgs {
		if !inLCS[a] && !c.unmatchedSource[a] {
			moves = append(moves, Move{a, matchings[a]})
		}
	}
	return moves
}

func (c *changeDistiller) computeMatchingSet() map[*exp.Expr]*exp.Expr {
	leaves := c.computeLeafMatchingSet()
	matching := map[*exp.Expr]*exp.Expr{}
	matchedTargetLeaves := map[*exp.Expr][]*exp.Expr{}
	for _, pair := range leaves {
		matching[pair[0]] = pair[1]
		matchedTargetLeaves[pair[0]] = append(matchedTargetLeaves[pair[0]], pair[1])
	}

	var orderedUnmatchedSource []*exp.Expr
	for n := range c.source.BFS(nil) {
		if c.unmatchedSource[n] {
			orderedUnmatchedSource = append(orderedUnmatchedSource, n)
		}
	}
	var orderedUnmatchedTarget []*exp.Expr
	seenTargets := map[*exp.Expr]bool{}
	for n := range c.target.BFS(nil) {
		if c.unmatchedTarget[n] && !seenTargets[n] {
			seenTargets[n] = true
			orderedUnmatchedTarget = append(orderedUnmatchedTarget, n)
		}
	}
	matchedTargets := map[*exp.Expr]bool{}

	targetLeafIDsCache := map[*exp.Expr]map[*exp.Expr]bool{}
	for _, sourceNode := range orderedUnmatchedSource {
		var sourceLeafIDs map[*exp.Expr]bool
		var candidateTargetLeaves []*exp.Expr
		sourceLeavesNum := 0
		for _, targetNode := range orderedUnmatchedTarget {
			if matchedTargets[targetNode] || !isSameType(sourceNode, targetNode) {
				continue
			}
			if sourceLeafIDs == nil {
				sourceLeafIDs = map[*exp.Expr]bool{}
				for _, l := range expressionLeaves(sourceNode) {
					if !sourceLeafIDs[l] {
						sourceLeafIDs[l] = true
						candidateTargetLeaves = append(candidateTargetLeaves, matchedTargetLeaves[l]...)
					}
				}
				sourceLeavesNum = len(sourceLeafIDs)
			}
			targetLeafIDs, ok := targetLeafIDsCache[targetNode]
			if !ok {
				targetLeafIDs = map[*exp.Expr]bool{}
				for _, l := range expressionLeaves(targetNode) {
					targetLeafIDs[l] = true
				}
				targetLeafIDsCache[targetNode] = targetLeafIDs
			}
			targetLeavesNum := len(targetLeafIDs)
			maxLeaves := max(sourceLeavesNum, targetLeavesNum)
			score := 0.0
			if maxLeaves > 0 {
				common := 0
				for _, t := range candidateTargetLeaves {
					if targetLeafIDs[t] {
						common++
					}
				}
				score = float64(common) / float64(maxLeaves)
			}
			adjustedT := c.t
			if min(sourceLeavesNum, targetLeavesNum) <= 4 {
				adjustedT = 0.4
			}
			if score >= 0.8 || (score >= adjustedT && c.diceCoefficient(sourceNode, targetNode) >= c.f) {
				matching[sourceNode] = targetNode
				delete(c.unmatchedSource, sourceNode)
				delete(c.unmatchedTarget, targetNode)
				matchedTargets[targetNode] = true
				delete(targetLeafIDsCache, targetNode)
				break
			}
		}
	}
	return matching
}

func (c *changeDistiller) computeLeafMatchingSet() [][2]*exp.Expr {
	type candidate struct {
		score, parentScore float64
		order              int
		source, target     *exp.Expr
	}
	var candidates []candidate
	sourceLeaves := expressionLeaves(c.source)
	targetLeaves := expressionLeaves(c.target)
	for _, s := range sourceLeaves {
		for _, t := range targetLeaves {
			if isSameType(s, t) {
				score := c.diceCoefficient(s, t)
				if score >= c.f {
					candidates = append(candidates, candidate{score, float64(parentSimilarityScore(s, t)), len(candidates), s, t})
				}
			}
		}
	}
	// pick the best matchings first: highest score, then highest parent similarity
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.parentScore != b.parentScore {
			return a.parentScore > b.parentScore
		}
		return a.order < b.order
	})
	var matching [][2]*exp.Expr
	for _, cand := range candidates {
		if c.unmatchedSource[cand.source] && c.unmatchedTarget[cand.target] {
			matching = append(matching, [2]*exp.Expr{cand.source, cand.target})
			delete(c.unmatchedSource, cand.source)
			delete(c.unmatchedTarget, cand.target)
		}
	}
	return matching
}

func (c *changeDistiller) diceCoefficient(source, target *exp.Expr) float64 {
	sourceHisto := c.bigramHisto(source)
	targetHisto := c.bigramHisto(target)
	total := 0
	for _, v := range sourceHisto {
		total += v
	}
	for _, v := range targetHisto {
		total += v
	}
	if total == 0 {
		if source.Equal(target) {
			return 1.0
		}
		return 0.0
	}
	overlap := 0
	for g, n := range sourceHisto {
		if m, ok := targetHisto[g]; ok {
			overlap += min(n, m)
		}
	}
	return 2 * float64(overlap) / float64(total)
}

func (c *changeDistiller) bigramHisto(e *exp.Expr) map[string]int {
	if h, ok := c.bigramHistoCache[e]; ok {
		return h
	}
	sql, err := c.dialect.Generate(e, exp.GenOpts(c.dialect, exp.Comments(false)))
	if err != nil {
		panic(err)
	}
	runes := []rune(sql)
	h := map[string]int{}
	for i := 0; i+1 < len(runes); i++ {
		h[string(runes[i:i+2])]++
	}
	c.bigramHistoCache[e] = h
	return h
}

func expressionLeaves(e *exp.Expr) []*exp.Expr {
	var out []*exp.Expr
	hasChild := false
	for _, n := range e.IterExpressions() {
		if !isIgnoredLeaf(n) {
			hasChild = true
			out = append(out, expressionLeaves(n)...)
		}
	}
	if !hasChild {
		out = append(out, e)
	}
	return out
}

// nonExpressionLeaves mirrors dict(_get_non_expression_leaves(e)).
func nonExpressionLeaves(e *exp.Expr) map[string]any {
	out := map[string]any{}
	for _, a := range e.Args() {
		switch v := a.Value.(type) {
		case nil, *exp.Expr:
			continue
		case []*exp.Expr:
			if len(v) > 0 {
				continue
			}
		case []any:
			if len(v) > 0 {
				if _, ok := v[0].(*exp.Expr); ok {
					continue
				}
			}
		}
		if rv := reflect.ValueOf(a.Value); rv.Kind() == reflect.Slice && rv.Len() == 0 {
			out[a.Key] = []any{} // every empty list compares equal
			continue
		}
		out[a.Key] = a.Value
	}
	return out
}

func isSameType(source, target *exp.Expr) bool {
	if source.Kind() != target.Kind() {
		return false
	}
	if source.IsKind(exp.Join) {
		return reflect.DeepEqual(source.Arg("side"), target.Arg("side"))
	}
	if source.IsKind(exp.Anonymous) {
		st, tt := source.Arg("this"), target.Arg("this")
		if se, ok := st.(*exp.Expr); ok {
			te, ok := tt.(*exp.Expr)
			return ok && se.Equal(te)
		}
		return st == tt
	}
	return true
}

func parentSimilarityScore(source, target *exp.Expr) int {
	if source == nil || target == nil || source.Kind() != target.Kind() {
		return 0
	}
	return 1 + parentSimilarityScore(source.Parent(), target.Parent())
}

func expressionOnlyArgs(e *exp.Expr) []*exp.Expr {
	var out []*exp.Expr
	for _, a := range e.IterExpressions() {
		if !isIgnoredLeaf(a) {
			out = append(out, a)
		}
	}
	return out
}

// lcs calculates the longest common subsequence.
func lcs(a, b []*exp.Expr, equal func(l, r *exp.Expr) bool) []*exp.Expr {
	prev := make([][]*exp.Expr, len(b)+1)
	for i := 1; i <= len(a); i++ {
		cur := make([][]*exp.Expr, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if equal(a[i-1], b[j-1]) {
				cur[j] = append(append([]*exp.Expr(nil), prev[j-1]...), a[i-1])
			} else if len(prev[j]) > len(cur[j-1]) {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev = cur
	}
	return prev[len(b)]
}
