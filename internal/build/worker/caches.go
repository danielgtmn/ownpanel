package worker

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// Caches. Each site has a folder of its own on the build disk, mounted at
// /cache in its builds (the build's home is in it, so package managers keep
// their downloads there). It is written by untrusted code, so it is only
// ever given to builds of the same site, and its size is limited by its own
// project quota (Config.CacheBytes). There are Config.Caches projects for
// caches; when all are taken, or the build disk runs low, the cache used
// longest ago is deleted.

// checkCaches removes cache folders that are not valid: a name that is not
// a site, a project outside the cache range, or a project two folders
// share.
func (w *Worker) checkCaches(ctx context.Context) error {
	entries, err := os.ReadDir(w.path(dirCaches))
	if err != nil {
		return err
	}
	seen := map[uint32]bool{}
	for _, e := range entries {
		dir := w.path(dirCaches, e.Name())
		ok := e.IsDir() && build.ValidSite(e.Name())
		if ok {
			p, err := w.cfg.Disk.Project(dir)
			ok = err == nil && p >= projectCacheBase && p < projectCacheBase+uint32(w.cfg.Caches) && !seen[p]
			seen[p] = ok || seen[p]
		}
		if !ok {
			w.log.Warn("removing a cache folder that is not valid", "name", e.Name())
			if err := w.cfg.Runtime.RemoveTree(ctx, dir); err != nil {
				return err
			}
		}
	}
	return nil
}

type cacheEntry struct {
	site    string
	project uint32
	used    time.Time
}

func (w *Worker) listCaches() ([]cacheEntry, error) {
	entries, err := os.ReadDir(w.path(dirCaches))
	if err != nil {
		return nil, err
	}
	var out []cacheEntry
	for _, e := range entries {
		if !e.IsDir() || !build.ValidSite(e.Name()) {
			continue
		}
		dir := w.path(dirCaches, e.Name())
		p, err := w.cfg.Disk.Project(dir)
		if err != nil {
			continue
		}
		info, err := os.Lstat(dir)
		if err != nil {
			continue
		}
		out = append(out, cacheEntry{site: e.Name(), project: p, used: info.ModTime()})
	}
	return out, nil
}

// claimCache keeps builds of site from starting while its cache is
// deleted; false if one runs. releaseCache ends that.
func (w *Worker) claimCache(site string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, j := range w.slots {
		if j != nil && j.spec.Site == site {
			return false
		}
	}
	if w.clearing[site] {
		return false
	}
	w.clearing[site] = true
	return true
}

func (w *Worker) releaseCache(site string) {
	w.mu.Lock()
	delete(w.clearing, site)
	w.mu.Unlock()
	w.poke()
}

// cacheFor returns the cache folder of site, making it if needed. Called by
// the run of a build of that site, so no other build uses it meanwhile.
func (w *Worker) cacheFor(ctx context.Context, site string) (string, error) {
	dir := w.path(dirCaches, site)
	now := w.cfg.Now()
	if _, err := os.Lstat(dir); err == nil {
		_ = os.Chtimes(dir, now, now)
		return dir, nil
	}
	caches, err := w.listCaches()
	if err != nil {
		return "", err
	}
	taken := map[uint32]bool{}
	for _, c := range caches {
		taken[c.project] = true
	}
	project := uint32(0)
	for n := range uint32(w.cfg.Caches) {
		if !taken[projectCacheBase+n] {
			project = projectCacheBase + n
			break
		}
	}
	if project == 0 {
		evicted, err := w.evictCache(ctx, site)
		if err != nil {
			return "", err
		}
		project = evicted
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	if err := w.cfg.Disk.SetProject(dir, project); err != nil {
		_ = os.Remove(dir)
		return "", err
	}
	return dir, nil
}

// evictCache deletes the cache used longest ago (never one of a running
// build, nor that of keep) and returns its project.
func (w *Worker) evictCache(ctx context.Context, keep string) (uint32, error) {
	caches, err := w.listCaches()
	if err != nil {
		return 0, err
	}
	slices.SortFunc(caches, func(a, b cacheEntry) int { return a.used.Compare(b.used) })
	for _, victim := range caches {
		if victim.site == keep || !w.claimCache(victim.site) {
			continue
		}
		w.log.Info("removing the build cache used longest ago", "site", victim.site)
		err := w.cfg.Runtime.RemoveTree(ctx, w.path(dirCaches, victim.site))
		w.releaseCache(victim.site)
		if err != nil {
			return 0, err
		}
		return victim.project, nil
	}
	return 0, errors.New("every cache is in use")
}

// makeRoom deletes caches, oldest first, until the build disk has need
// bytes free.
func (w *Worker) makeRoom(ctx context.Context, need int64, keep string) error {
	for {
		u, err := w.cfg.Disk.Usage(w.cfg.Work)
		if err != nil {
			return err
		}
		if u.FreeBytes >= need {
			return nil
		}
		if _, err := w.evictCache(ctx, keep); err != nil {
			return errors.New("the build disk is full")
		}
	}
}

// Caches lists the caches and their sizes.
func (w *Worker) Caches() ([]build.Cache, error) {
	caches, err := w.listCaches()
	if err != nil {
		return nil, err
	}
	out := make([]build.Cache, 0, len(caches))
	for _, c := range caches {
		bc := build.Cache{Site: c.site}
		if u, err := w.cfg.Disk.Usage(w.path(dirCaches, c.site)); err == nil {
			bc.Bytes = u.UsedBytes()
			bc.Files = u.Files - u.FreeFiles
		}
		out = append(out, bc)
	}
	return out, nil
}

// ClearCache deletes the cache of a site; not while one of its builds runs.
func (w *Worker) ClearCache(ctx context.Context, site string) error {
	if !build.ValidSite(site) {
		return errorf(statusBadRequest, "the site name is not valid")
	}
	if !w.claimCache(site) {
		return errorf(statusConflict, "a build of %s is running; clear its cache when it has ended", site)
	}
	defer w.releaseCache(site)
	dir := w.path(dirCaches, site)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return w.cfg.Runtime.RemoveTree(ctx, dir)
}
