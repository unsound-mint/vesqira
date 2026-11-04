# ADR 0023: Review threads, places on the picture, and resolve

Status: accepted under delegated merge; owner review pending.

## Context
A reviewer could leave a flat comment on a figure; the author could not answer inside the tool and nothing said whether a point was settled.

## Decision
- A comment may carry a place on the published picture: a point or a box in fractions of the picture's width and height. It is validated (finite, inside 0..1, a box has positive size) and always described as a place on the picture as drawn, not a sky position: the reviewer never has the data, and the preview is a rendering.
- A thread is one level deep: a first comment and its replies, which inherit the figure and cannot carry a place of their own. A reply can only target a first comment of the same snapshot; the same 500-comment limit applies.
- The author replies (`POST /api/snapshots/{id}/comments`) and closes or reopens a thread (`POST /api/comments/{id}/resolve`) through the owner API. Reviewers can reply but not resolve: whether a point is settled is the author's call. The review listener mounts neither route. A reviewer sees replies, places and the closed state.
- The marking control works with a pointer (click for a point, drag for a box) and with the keyboard (arrows and Enter for a point); a box has no keyboard route yet.

## Alternatives
Anchoring to sky coordinates (would need the data or a mapping the reviewer must not have); unlimited nesting (not needed for a review of figures); letting the reviewer resolve (common elsewhere, but here the author decides when a scientific question is answered).

## Consequences
Implemented. The `comments` table gains `parent_id`, `anchor`, `from_owner` and `resolved_at` columns, added if missing. Names are still self-declared: reviewer identity and authentication are a later stage (roadmap stage 7).

## Validation
Store tests for validation, one-level threads, cross-snapshot isolation, resolve rules; a server test through both listeners including 404s and that owner routes are unreachable on the review surface; component tests for rendering, replying, and marking with the keyboard.

## Rollback/Migration
Columns are additive; older readers ignore them.

## Amendment: what is new since the last visit
A snapshot reports how many reviewer comments it has and when the latest reviewer comment or outcome happened (`comments`, `lastActivity`); the author's own replies do not count. Each side keeps, in its own browser only, when it last looked: the author's board screen shows "New since you last looked" and marks new reviewer comments; the review page marks the author's replies made since the reviewer's last visit and remembers the reviewer's name. These are conveniences kept in browser storage, not shared state: a browser that will not store them shows nothing as new.
