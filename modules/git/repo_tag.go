// Copyright 2015 The Gogs Authors. All rights reserved.
// Copyright 2019 The Gitea Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package git

import (
	"context"
	"fmt"
	"io"
	"strings"

	"code.gitea.io/gitea/modules/git/foreachref"
	"code.gitea.io/gitea/modules/util"
)

// TagPrefix tags prefix path on the repository
const TagPrefix = "refs/tags/"

// IsTagExist returns true if given tag exists in the repository.
func IsTagExist(ctx context.Context, repoPath, name string) bool {
	return IsReferenceExist(ctx, repoPath, TagPrefix+name)
}

// CreateTag create one tag in the repository
func (repo *Repository) CreateTag(name, revision string) error {
	_, err := NewCommand(repo.Ctx, "tag", "--", name, revision).RunInDir(repo.Path)
	return err
}

// CreateAnnotatedTag create one annotated tag in the repository
func (repo *Repository) CreateAnnotatedTag(name, message, revision string) error {
	_, err := NewCommand(repo.Ctx, "tag", "-a", "-m", message, "--", name, revision).RunInDir(repo.Path)
	return err
}

// GetTagNameBySHA returns the name of a tag from its tag object SHA or commit SHA
func (repo *Repository) GetTagNameBySHA(sha string) (string, error) {
	if len(sha) < 5 {
		return "", fmt.Errorf("SHA is too short: %s", sha)
	}

	stdout, err := NewCommand(repo.Ctx, "show-ref", "--tags", "-d").RunInDir(repo.Path)
	if err != nil {
		return "", err
	}

	tagRefs := strings.Split(stdout, "\n")
	for _, tagRef := range tagRefs {
		if len(strings.TrimSpace(tagRef)) > 0 {
			fields := strings.Fields(tagRef)
			if strings.HasPrefix(fields[0], sha) && strings.HasPrefix(fields[1], TagPrefix) {
				name := fields[1][len(TagPrefix):]
				// annotated tags show up twice, we should only return if is not the ^{} ref
				if !strings.HasSuffix(name, "^{}") {
					return name, nil
				}
			}
		}
	}
	return "", ErrNotExist{ID: sha}
}

// GetTagID returns the object ID for a tag (annotated tags have both an object SHA AND a commit SHA)
func (repo *Repository) GetTagID(name string) (string, error) {
	stdout, err := NewCommand(repo.Ctx, "show-ref", "--tags", "--", name).RunInDir(repo.Path)
	if err != nil {
		return "", err
	}
	// Make sure exact match is used: "v1" != "release/v1"
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "refs/tags/"+name {
			return fields[0], nil
		}
	}
	return "", ErrNotExist{ID: name}
}

// GetTag returns a Git tag by given name.
func (repo *Repository) GetTag(name string) (*Tag, error) {
	idStr, err := repo.GetTagID(name)
	if err != nil {
		return nil, err
	}

	id, err := NewIDFromString(idStr)
	if err != nil {
		return nil, err
	}

	tag, err := repo.getTag(id, name)
	if err != nil {
		return nil, err
	}
	return tag, nil
}

// GetTagWithID returns a Git tag by given name and ID
func (repo *Repository) GetTagWithID(idStr, name string) (*Tag, error) {
	id, err := NewIDFromString(idStr)
	if err != nil {
		return nil, err
	}

	tag, err := repo.getTag(id, name)
	if err != nil {
		return nil, err
	}
	return tag, nil
}

// GetTagInfos returns all tag infos of the repository.
func (repo *Repository) GetTagInfos(page, pageSize int) ([]*Tag, int, error) {
	forEachRefFmt := foreachref.NewFormat("objecttype", "refname:short", "object", "objectname", "creator", "contents", "contents:signature")

	stdoutReader, stdoutWriter := io.Pipe()
	defer stdoutReader.Close()
	defer stdoutWriter.Close()
	stderr := strings.Builder{}
	rc := &RunContext{Dir: repo.Path, Stdout: stdoutWriter, Stderr: &stderr, Timeout: -1}

	go func() {
		err := NewCommand(repo.Ctx, "for-each-ref", "--format", forEachRefFmt.Flag(), "--sort", "-*creatordate", "refs/tags").RunWithContext(rc)
		if err != nil {
			_ = stdoutWriter.CloseWithError(ConcatenateError(err, stderr.String()))
		} else {
			_ = stdoutWriter.Close()
		}
	}()

	var tags []*Tag
	parser := forEachRefFmt.Parser(stdoutReader)
	for {
		ref := parser.Next()
		if ref == nil {
			break
		}

		tag, err := parseTagRef(ref)
		if err != nil {
			return nil, 0, fmt.Errorf("GetTagInfos: parse tag: %w", err)
		}
		tags = append(tags, tag)
	}
	if err := parser.Err(); err != nil {
		return nil, 0, fmt.Errorf("GetTagInfos: parse output: %w", err)
	}

	sortTagsByTime(tags)
	tagsTotal := len(tags)
	if page != 0 {
		tags = util.PaginateSlice(tags, page, pageSize).([]*Tag)
	}

	return tags, tagsTotal, nil
}

// parseTagRef parses a tag from a 'git for-each-ref'-produced reference.
func parseTagRef(ref map[string]string) (tag *Tag, err error) {
	tag = &Tag{
		Type: ref["objecttype"],
		Name: ref["refname:short"],
	}

	tag.ID, err = NewIDFromString(ref["objectname"])
	if err != nil {
		return nil, fmt.Errorf("parse objectname '%s': %v", ref["objectname"], err)
	}

	if tag.Type == "commit" {
		// lightweight tag
		tag.Object = tag.ID
	} else {
		// annotated tag
		tag.Object, err = NewIDFromString(ref["object"])
		if err != nil {
			return nil, fmt.Errorf("parse object '%s': %v", ref["object"], err)
		}
	}

	tag.Tagger, err = newSignatureFromCommitline([]byte(ref["creator"]))
	if err != nil {
		return nil, fmt.Errorf("parse tagger: %w", err)
	}

	tag.Message = ref["contents"]
	// strip PGP signature if present in contents field
	pgpStart := strings.Index(tag.Message, beginpgp)
	if pgpStart >= 0 {
		tag.Message = tag.Message[0:pgpStart]
	}

	// annotated tag with GPG signature
	if tag.Type == "tag" && ref["contents:signature"] != "" {
		payload := fmt.Sprintf("object %s\ntype commit\ntag %s\ntagger %s\n\n%s\n",
			tag.Object, tag.Name, ref["creator"], strings.TrimSpace(tag.Message))
		tag.Signature = &CommitGPGSignature{
			Signature: ref["contents:signature"],
			Payload:   payload,
		}
	}

	return tag, nil
}

// GetAnnotatedTag returns a Git tag by its SHA, must be an annotated tag
func (repo *Repository) GetAnnotatedTag(sha string) (*Tag, error) {
	id, err := NewIDFromString(sha)
	if err != nil {
		return nil, err
	}

	// Tag type must be "tag" (annotated) and not a "commit" (lightweight) tag
	if tagType, err := repo.GetTagType(id); err != nil {
		return nil, err
	} else if ObjectType(tagType) != ObjectTag {
		// not an annotated tag
		return nil, ErrNotExist{ID: id.String()}
	}

	// Get tag name
	name, err := repo.GetTagNameBySHA(id.String())
	if err != nil {
		return nil, err
	}

	tag, err := repo.getTag(id, name)
	if err != nil {
		return nil, err
	}
	return tag, nil
}
