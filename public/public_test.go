package public

import (
	"context"
	"fmt"
	"io/fs"
	"io/ioutil"
	"os"
	"testing"

	cmp "github.com/google/go-cmp/cmp"
	cmpopts "github.com/google/go-cmp/cmp/cmpopts"
	cid "github.com/ipfs/go-cid"
	golog "github.com/ipfs/go-log"
	base "github.com/qri-io/wnfs-go/base"
	mdstore "github.com/qri-io/wnfs-go/mdstore"
	mdstoremock "github.com/qri-io/wnfs-go/mdstore/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	if lvl := os.Getenv("WNFS_LOGGING"); lvl != "" {
		golog.SetLogLevel("wnfs", lvl)
	}
}

func TestTreeSkeleton(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newMemTestStore(ctx, t)
	fs := mdfs{ctx: ctx, ds: store}

	root := NewEmptyTree(fs, "")
	root.Add(base.MustPath("foo/bar/baz/hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello!")))
	root.Add(base.MustPath("bar/baz/goodbye"), base.NewMemfileBytes("goodbye", []byte(`goodbye`)))
	root.Add(base.MustPath("some.json"), base.NewMemfileBytes("some.json", []byte(`{"oh":"hai}`)))

	expect := base.Skeleton{
		"bar": base.SkeletonInfo{
			SubSkeleton: base.Skeleton{
				"baz": base.SkeletonInfo{
					SubSkeleton: base.Skeleton{
						"goodbye": base.SkeletonInfo{IsFile: true},
					},
				},
			},
		},
		"foo": base.SkeletonInfo{
			SubSkeleton: base.Skeleton{
				"bar": base.SkeletonInfo{
					SubSkeleton: base.Skeleton{
						"baz": base.SkeletonInfo{
							SubSkeleton: base.Skeleton{
								"hello.txt": base.SkeletonInfo{IsFile: true},
							},
						},
					},
				},
			},
		},
		"some.json": base.SkeletonInfo{IsFile: true},
	}

	got, err := root.Skeleton()
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(expect, got, cmpopts.IgnoreTypes(cid.Cid{})); diff != "" {
		t.Errorf("result mismatch (-want +got):\n%s", diff)
	}
}

func TestHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := newMemTestStore(ctx, t)
	fs := mdfs{ctx: ctx, ds: store}

	tree := NewEmptyTree(fs, "a")
	_, err := tree.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello!")))
	require.Nil(t, err)
	_, err = tree.Add(base.MustPath("salut.txt"), base.NewMemfileBytes("hello.txt", []byte("salut!")))
	require.Nil(t, err)
	_, err = tree.Add(base.MustPath("salut.txt"), base.NewMemfileBytes("hello.txt", []byte("salut 2!")))
	require.Nil(t, err)
	_, err = tree.Add(base.MustPath("dir/goodbye.txt"), base.NewMemfileBytes("goodbye.txt", []byte("goodbye!")))
	require.Nil(t, err)
	_, err = tree.Add(base.MustPath("dir/goodbye.txt"), base.NewMemfileBytes("goodbye.txt", []byte("goodbye 2!")))
	require.Nil(t, err)
	_, err = tree.Add(base.MustPath("dir/bonjour.txt"), base.NewMemfileBytes("bonjour.txt", []byte("bonjour!")))
	require.Nil(t, err)

	hist := mustHistCids(t, tree)
	assert.Equal(t, 6, len(hist))

	salut, err := tree.Get(base.Path{"salut.txt"})
	require.Nil(t, err)
	hist = mustHistCidsFile(t, salut.(*PublicFile))
	assert.Equal(t, 2, len(hist))

	dir, err := tree.Get(base.Path{"dir"})
	require.Nil(t, err)
	hist = mustHistCids(t, dir.(*PublicTree))
	assert.Equal(t, 3, len(hist))

	goodbye, err := tree.Get(base.Path{"dir", "goodbye.txt"})
	require.Nil(t, err)
	hist = mustHistCidsFile(t, goodbye.(*PublicFile))
	assert.Equal(t, 2, len(hist))

	bonjour, err := tree.Get(base.Path{"dir", "bonjour.txt"})
	require.Nil(t, err)
	hist = mustHistCidsFile(t, bonjour.(*PublicFile))
	assert.Equal(t, 1, len(hist))
}

func TestBasicTreeMerge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := newMemTestStore(ctx, t)
	fs := mdfs{ctx: ctx, ds: store}

	t.Run("no_common_history", func(t *testing.T) {
		a := NewEmptyTree(fs, "")
		a.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello!")))

		b := NewEmptyTree(fs, "")

		_, err := a.Merge(b)
		assert.ErrorIs(t, err, base.ErrNoCommonHistory)
	})

	t.Run("fast_forward", func(t *testing.T) {
		// local node is behind, fast-forward
		a := NewEmptyTree(fs, "a")
		_, err := a.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello!")))
		require.Nil(t, err)

		b, err := LoadTreeFromCID(a.fs, a.Name(), a.Cid())
		require.Nil(t, err)
		_, err = b.Add(base.MustPath("goodbye.txt"), base.NewMemfileBytes("goodbye.txt", []byte("goodbye!")))
		require.Nil(t, err)

		res, err := a.Merge(b)
		require.Nil(t, err)
		assert.Equal(t, base.MTFastForward, res.Type)
	})

	t.Run("local_ahead", func(t *testing.T) {
		a := NewEmptyTree(fs, "")
		_, err := a.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello!")))
		require.Nil(t, err)

		b, err := LoadTreeFromCID(a.fs, a.Name(), a.Cid())
		require.Nil(t, err)

		// local node is ahead, no-op for local merge
		_, err = a.Add(base.MustPath("goodbye.txt"), base.NewMemfileBytes("goodbye.txt", []byte("goodbye!")))
		require.Nil(t, err)

		res, err := a.Merge(b)
		require.Nil(t, err)
		assert.Equal(t, base.MTLocalAhead, res.Type)
	})
}

func TestTreeMergeCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := newMemTestStore(ctx, t)
	fs := mdfs{ctx: ctx, ds: store}

	t.Run("no_conflict_merge", func(t *testing.T) {
		a := NewEmptyTree(fs, "")
		_, err := a.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello!")))
		require.Nil(t, err)

		b, err := LoadTreeFromCID(a.fs, a.Name(), a.Cid())
		require.Nil(t, err)

		_, err = a.Add(base.MustPath("bonjour.txt"), base.NewMemfileBytes("bonjour.txt", []byte("bonjour!")))
		require.Nil(t, err)

		// local node is ahead, no-op for local merge
		_, err = b.Add(base.MustPath("goodbye.txt"), base.NewMemfileBytes("goodbye.txt", []byte("goodbye!")))
		require.Nil(t, err)

		res, err := a.Merge(b)
		require.Nil(t, err)
		assert.Equal(t, base.MTMergeCommit, res.Type)
		assert.NotNil(t, a.merge)
		mustDirChildren(t, a, []string{
			"bonjour.txt",
			"goodbye.txt",
			"hello.txt",
		})
		mustFileContents(t, a, "goodbye.txt", "goodbye!")
		mustFileContents(t, a, "hello.txt", "hello!")
	})

	t.Run("remote_overwrites_local_file", func(t *testing.T) {
		a := NewEmptyTree(fs, "")
		_, err := a.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello!")))
		require.Nil(t, err)

		b, err := LoadTreeFromCID(a.fs, a.Name(), a.Cid())
		require.Nil(t, err)

		_, err = b.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello **2**, written on remote")))
		require.Nil(t, err)

		// add to a to diverge histories
		_, err = a.Add(base.MustPath("goodbye.txt"), base.NewMemfileBytes("goodbye.txt", []byte("goodbye!")))
		require.Nil(t, err)

		res, err := a.Merge(b)
		require.Nil(t, err)
		assert.Equal(t, base.MTMergeCommit, res.Type)
		mustDirChildren(t, a, []string{
			"goodbye.txt",
			"hello.txt",
		})
		mustFileContents(t, a, "hello.txt", "hello **2**, written on remote")
	})

	t.Run("local_overwrites_remote_file", func(t *testing.T) {
		a := NewEmptyTree(fs, "")
		_, err := a.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello!")))
		require.Nil(t, err)

		b, err := LoadTreeFromCID(a.fs, a.Name(), a.Cid())
		require.Nil(t, err)
		_, err = b.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello **2** (remote)")))
		require.Nil(t, err)

		// a has more commits, should win
		_, err = a.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello **2**")))
		require.Nil(t, err)
		_, err = a.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello **3**")))
		require.Nil(t, err)

		res, err := a.Merge(b)
		require.Nil(t, err)
		assert.Equal(t, base.MTMergeCommit, res.Type)
		mustDirChildren(t, a, []string{
			"hello.txt",
		})
		mustFileContents(t, a, "hello.txt", "hello **3**")
	})

	t.Run("remote_deletes_local_file", func(t *testing.T) {
		a := NewEmptyTree(fs, "")
		_, err := a.Add(base.MustPath("hello.txt"), base.NewMemfileBytes("hello.txt", []byte("hello!")))
		require.Nil(t, err)

		b, err := LoadTreeFromCID(a.fs, a.Name(), a.Cid())
		require.Nil(t, err)
		_, err = b.Rm(base.MustPath("hello.txt"))
		require.Nil(t, err)

		// add to a to diverge histories
		_, err = a.Add(base.MustPath("goodbye.txt"), base.NewMemfileBytes("goodbye.txt", []byte("goodbye!")))
		require.Nil(t, err)

		res, err := a.Merge(b)
		require.Nil(t, err)
		assert.Equal(t, base.MTMergeCommit, res.Type)
		mustDirChildren(t, a, []string{
			"goodbye.txt",
		})
	})

	t.Run("local_deletes_remote_file", func(t *testing.T) {
		t.Skip("TODO(b5)")
	})
	t.Run("remote_deletes_local_dir", func(t *testing.T) {
		t.Skip("TODO(b5)")
	})
	t.Run("local_deletes_remote_dir", func(t *testing.T) {
		t.Skip("TODO(b5)")
	})

	t.Run("remote_overwrites_local_file_with_directory", func(t *testing.T) {
		t.Skip("TODO(b5)")
	})
	t.Run("local_overwrites_remote_file_with_directory", func(t *testing.T) {
		t.Skip("TODO(b5)")
	})

	t.Run("remote_overwrites_local_directory_with_file", func(t *testing.T) {
		t.Skip("TODO(b5)")
	})
	t.Run("local_overwrites_remote_directory_with_file", func(t *testing.T) {
		t.Skip("TODO(b5)")
	})

	t.Run("remote_delete_undeleted_by_local_edit", func(t *testing.T) {
		t.Skip("TODO(b5)")
	})
	t.Run("local_delete_undeleted_by_remote_edit", func(t *testing.T) {
		t.Skip("TODO(b5)")
	})

	t.Run("merge_remote_into_local_then_sync_local_to_remote", func(t *testing.T) {
		t.Skip("TODO(b5)")
	})
}

type mdfs struct {
	ctx context.Context
	ds  mdstore.MerkleDagStore
}

var _ base.MerkleDagFS = (*mdfs)(nil)

func (fs mdfs) Open(path string) (fs.File, error) { return nil, fmt.Errorf("shim MDFS cannot open") }
func (fs mdfs) Context() context.Context          { return fs.ctx }
func (fs mdfs) DagStore() mdstore.MerkleDagStore  { return fs.ds }

type fataler interface {
	Name() string
	Helper()
	Fatal(args ...interface{})
}

func newMemTestStore(ctx context.Context, f fataler) mdstore.MerkleDagStore {
	f.Helper()
	store, err := mdstore.NewMerkleDagStore(ctx, mdstoremock.NewOfflineMemBlockservice())
	if err != nil {
		f.Fatal(err)
	}
	return store
}

func mustHistCids(t *testing.T, tree *PublicTree) []cid.Cid {
	t.Helper()
	log, err := base.History(context.Background(), tree.fs.DagStore(), tree, -1)
	require.Nil(t, err)
	ids := make([]cid.Cid, len(log))
	for i, l := range log {
		ids[i] = l.Cid
	}
	return ids
}

// TODO(b5): base.Node interface needs work, this and mustHistCids should be one
// function
func mustHistCidsFile(t *testing.T, f *PublicFile) []cid.Cid {
	t.Helper()
	log, err := base.History(context.Background(), f.fs.DagStore(), f, -1)
	require.Nil(t, err)
	ids := make([]cid.Cid, len(log))
	for i, l := range log {
		ids[i] = l.Cid
	}
	return ids
}

func mustDirChildren(t *testing.T, dir *PublicTree, ch []string) {
	t.Helper()
	ents, err := dir.ReadDir(-1)
	require.Nil(t, err)

	got := make([]string, 0, len(ents))
	for _, ch := range ents {
		got = append(got, ch.Name())
	}

	assert.Equal(t, ch, got)
}

func mustFileContents(t *testing.T, dir *PublicTree, path, content string) {
	t.Helper()
	f, err := dir.Get(base.MustPath(path))
	require.Nil(t, err)
	defer f.Close()

	data, err := ioutil.ReadAll(f)
	require.Nil(t, err)

	assert.Equal(t, content, string(data))
}
