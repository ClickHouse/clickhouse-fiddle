package dockertag

import (
	"context"
	"maps"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lodthe/clickhouse-playground/pkg/chspec"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

type RegistryClient interface {
	// GetTags returns all tags of the repository.
	GetTags(repository string) ([]string, error)
	// GetDigest resolves the manifest digest the tag currently points to.
	GetDigest(repository string, tag string) (string, error)
}

// Cache is a cache for the list of docker image's tags.
type Cache struct {
	ctx    context.Context
	config Config
	logger zerolog.Logger
	cli    RegistryClient

	updating atomic.Int32

	mu         sync.RWMutex
	updatedAt  time.Time
	imageByTag map[string]Image
	images     []Image
}

func NewCache(ctx context.Context, config Config, logger zerolog.Logger, cli RegistryClient) *Cache {
	return &Cache{
		ctx:        ctx,
		config:     config,
		logger:     logger,
		cli:        cli,
		imageByTag: make(map[string]Image),
	}
}

// RunBackgroundUpdate runs a background task that keeps data actual.
func (c *Cache) RunBackgroundUpdate() {
	go c.backgroundUpdate()
}

func (c *Cache) backgroundUpdate() {
	update := func() {
		c.mu.RLock()
		defer c.mu.RUnlock()

		c.updateIfExpired()
	}

	c.logger.Info().Msg("docker tag cache update background task has been started")

	update()
	t := time.NewTicker(c.config.ExpirationTime)
	defer t.Stop()

	for {
		select {
		case <-c.ctx.Done():
			c.logger.Info().Msg("docker tag cache update background task has been finished")
			return

		case <-t.C:
		}

		update()
	}
}

func (c *Cache) normalizeTag(tag string) string {
	return strings.ToLower(strings.TrimSpace(tag))
}

// GetAll returns all known tags for the given image.
func (c *Cache) GetAll() []Image {
	c.mu.RLock()
	defer c.mu.RUnlock()

	c.updateIfExpired()

	return c.images
}

// Exists checks whether the image has the given tag.
func (c *Cache) Exists(tag string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	_, found := c.imageByTag[c.normalizeTag(tag)]

	c.updateIfExpired()

	return found
}

// Find searches an image by its tag.
//
// The image digest is resolved lazily on the first call and cached until the next cache
// refresh, so mutable tags (head, latest, major.minor) are re-resolved once per expiration
// period. A digest resolution failure is not fatal: the image is returned with an empty
// digest and the caller decides.
func (c *Cache) Find(tag string) (Image, bool) {
	key := c.normalizeTag(tag)

	c.mu.RLock()
	img, found := c.imageByTag[key]
	c.updateIfExpired()
	c.mu.RUnlock()

	if !found || img.Digest != "" {
		return img, found
	}

	digest, err := c.cli.GetDigest(img.Repository, img.Tag)
	if err != nil {
		c.logger.Error().Err(err).Str("repository", img.Repository).Str("tag", img.Tag).Msg("failed to resolve image digest")

		return img, true
	}

	img.Digest = digest

	c.mu.Lock()
	// A concurrent refresh may have replaced the entry; only fill the resolved digest in.
	if cur, ok := c.imageByTag[key]; ok && cur.Repository == img.Repository && cur.Digest == "" {
		c.imageByTag[key] = img
	}
	c.mu.Unlock()

	return img, true
}

// updateIfExpired asynchronously updates cache if the cache has expired.
// The function should be called under the acquired mu lock.
func (c *Cache) updateIfExpired() {
	if time.Since(c.updatedAt) < c.config.ExpirationTime {
		return
	}

	// Do nothing if the updating lock has been acquired in another goroutine.
	if !c.updating.CompareAndSwap(0, 1) {
		return
	}

	go c.asyncUpdate()
}

// asyncUpdate fetches actual image list and updates the cache.
//
// The updating atomic is used to prevent simultaneous updates.
func (c *Cache) asyncUpdate() {
	// Release the acquired lock.
	defer func() {
		c.updating.Store(0)
	}()

	_ = c.fetchAndStore()
}

// Update synchronously fetches the image list and refreshes the cache. It is intended for a
// blocking initial load at startup so the server does not reject valid versions while the
// first background fetch is still in flight.
func (c *Cache) Update() error {
	return c.fetchAndStore()
}

func (c *Cache) fetchAndStore() error {
	startedAt := time.Now()

	images, imgByTag, err := c.getImagesFromSeveralRepositories(c.config.Repositories)
	if err != nil {
		return err
	}

	func() {
		c.mu.Lock()
		defer c.mu.Unlock()

		c.updatedAt = time.Now()
		c.images = images
		c.imageByTag = imgByTag
	}()

	c.logger.Debug().Dur("elapsed", time.Since(startedAt)).Int("tag_count", len(imgByTag)).Msg("docker image cache has been updated")

	return nil
}

// getImagesFromSeveralRepositories fetches images from the given list of repositories.
//
// It spawns a goroutine for each repository that collects images from it.
// Then it merges all the lists of images. If there are several occurrences of an image tag in two repositories,
// the data is taken from the first repository.
//
// It returns a list of images and a map that links an image to its tag.
func (c *Cache) getImagesFromSeveralRepositories(repositories []string) ([]Image, map[string]Image, error) {
	g, _ := errgroup.WithContext(c.ctx)
	imagesByRepo := make([][]Image, len(repositories))
	for i := range repositories {
		g.Go(func() error {
			images, err := c.getImages(repositories[i])
			if err != nil {
				return err
			}

			imagesByRepo[i] = images

			return nil
		})
	}

	err := g.Wait()
	if err != nil {
		c.logger.Err(err).Msg("failed to update docker image cache")
		return nil, nil, err
	}

	imgByTag := make(map[string]Image)
	for _, images := range imagesByRepo {
		for _, img := range images {
			tag := c.normalizeTag(img.Tag)

			// If a tag is presented in several repositories, we save image from the first repo.
			_, exists := imgByTag[tag]
			if exists {
				continue
			}

			imgByTag[tag] = img
		}
	}

	return c.sortImages(imgByTag), imgByTag, nil
}

// getImages returns a list of images from the given repository.
func (c *Cache) getImages(repository string) ([]Image, error) {
	tags, err := c.cli.GetTags(repository)
	if err != nil {
		c.logger.Error().Err(err).Str("repository", repository).Msg("failed to get registry tags")
		return nil, errors.Wrap(err, "failed to get tags from the registry")
	}

	images := make([]Image, 0, len(tags))
	for _, tag := range tags {
		images = append(images, Image{
			Repository: repository,
			Tag:        tag,
		})
	}

	c.logger.Debug().Str("repository", repository).Int("count", len(images)).Msg("images have been fetched")

	return images, nil
}

var headOfListTags = []string{
	"head-alpine",
	"head",
	"latest-alpine",
	"latest",
}

var tagsToDrop = []string{
	"12334",
	"12334-eefeec2519f5bdfec4516395a684ff570b5560a6",
}

// sortImages orders image list in human-readable order.
func (c *Cache) sortImages(imgByTag map[string]Image) []Image {
	copied := make(map[string]Image, len(imgByTag))
	maps.Copy(copied, imgByTag)
	imgByTag = copied

	// Drop whitelisted tags and remember potential head of the list.
	for _, tag := range tagsToDrop {
		delete(imgByTag, tag)
	}

	seenHeadOfList := make(map[string]Image, len(headOfListTags))
	for _, tag := range headOfListTags {
		img, found := imgByTag[tag]
		if !found {
			continue
		}

		seenHeadOfList[tag] = img
		delete(imgByTag, tag)
	}

	images := make([]Image, 0, len(imgByTag))
	for _, i := range imgByTag {
		images = append(images, i)
	}

	sortedImages := make([]Image, 0, len(seenHeadOfList)+len(images))

	// Split a tag by '.' and save this representation to use it in comparator.
	parsed := make([]chspec.Semver, len(images))
	ids := make([]int, len(images))
	for id, img := range images {
		parsed[id] = chspec.Parse(img.Tag)
		ids[id] = id
	}

	sort.Slice(ids, func(i, j int) bool {
		return chspec.IsGreater(parsed[ids[i]], parsed[ids[j]])
	})

	// At first, head of list images must be added.
	for _, tag := range headOfListTags {
		img, found := seenHeadOfList[tag]
		if !found {
			continue
		}

		sortedImages = append(sortedImages, img)
	}

	for _, id := range ids {
		sortedImages = append(sortedImages, images[id])
	}

	return sortedImages
}
