package server

import (
	"encoding/json"
	"fmt"

	"github.com/unsound-mint/vesqira/services/api/internal/access"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/publish"
	"github.com/unsound-mint/vesqira/services/api/internal/pushwire"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Access labels on pushed work (ADR 0069). A figure, a board or a report pushed to the group server carries the label the local
// instance declared for its data, in the document, so it is part of the item's hash and a changed label is a new revision. Pushing
// into a workspace is how work becomes visible to its members, so the server applies the workspace's publishing policy to it:
// an item that declares a blocked label is refused. The server cannot see the data; it enforces what is declared.

// pushedLabels lists the labels a pushed document declares: a figure's, a board's and each of its panels', a report revision's.
// A document that cannot be read has none (checkPushedDoc has already refused it).
func pushedLabels(it pushwire.Item) []string {
	switch it.Kind {
	case pushwire.KindFigure:
		var f pushedFigure
		if json.Unmarshal(it.Doc, &f) == nil {
			return []string{f.Access}
		}
	case pushwire.KindBoard:
		var b pushedBoard
		if json.Unmarshal(it.Doc, &b) == nil {
			out := []string{b.Access}
			for _, x := range b.Items {
				out = append(out, x.Access)
			}
			return out
		}
	case pushwire.KindReport:
		var r publish.Revision
		if json.Unmarshal(it.Doc, &r) == nil {
			return []string{r.Access}
		}
	}
	return nil
}

// checkPushedLabels refuses a label that is not canonical: empty (released), "collaboration" or "unreleased". "none" is written as
// nothing, so one meaning has one encoding and one hash.
func checkPushedLabels(it pushwire.Item) error {
	for _, l := range pushedLabels(it) {
		if l != "" && (!access.Label(l).Valid() || !access.Label(l).Restricted()) {
			return fmt.Errorf("an access label is collaboration or unreleased, or left out when the data is released")
		}
	}
	return nil
}

// pushedAccessRefusal applies the workspace's policy to a pushed item: an item that declares a label the admin has made block
// publishing is refused with the sentence the pusher sees.
func (s *Server) pushedAccessRefusal(ws workspace.ID, it pushwire.Item) error {
	blocked := s.blockedLabels(ws)
	for _, l := range pushedLabels(it) {
		if access.Blocks(blocked, access.Label(l)) {
			return fmt.Errorf("this workspace does not accept work from data labelled %s: its admin has made that label block publishing", access.Label(l).Name())
		}
	}
	return nil
}

// stampPushedBoard writes the labels of a board's figures onto the frozen panels and returns the board's own (empty when released).
func (s *Server) stampPushedBoard(ws workspace.ID, b boards.Board, items []boards.FrozenItem) (boardAccess, error) {
	v, err := s.accessView(ws)
	if err != nil {
		return boardAccess{}, fmt.Errorf("the access labels could not be read: %v", err)
	}
	ba := s.labelsOfBoard(v, b.Items)
	boards.StampAccess(items, b.Items, ba.labels())
	return ba, nil
}
