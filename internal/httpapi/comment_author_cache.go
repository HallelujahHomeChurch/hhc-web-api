package httpapi

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const commentAuthorTTL = time.Minute
const maxCommentAuthors = 1024

// Only presentation data is cached; recording access and per-viewer state are not.
type commentAuthor struct {
	ID, DisplayName, AvatarURL string
}
type cachedCommentAuthor struct {
	profile commentAuthor
	expires time.Time
}
type commentAuthorCache struct {
	mu      sync.Mutex
	entries map[string]cachedCommentAuthor
	loads   singleflight.Group
	now     func() time.Time
}

func (c *commentAuthorCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *commentAuthorCache) read(ids []string) (map[string]commentAuthor, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	profiles := make(map[string]commentAuthor, len(ids))
	missing := []string{}
	now := c.clock()
	for _, id := range ids {
		if item, ok := c.entries[id]; ok && now.Before(item.expires) {
			profiles[id] = item.profile
		} else {
			missing = append(missing, id)
		}
	}
	return profiles, missing
}

func (c *commentAuthorCache) resolve(ctx context.Context, resolver investigationIdentityResolver, ids []string) (map[string]commentAuthor, error) {
	profiles, missing := c.read(ids)
	if len(missing) == 0 {
		return profiles, nil
	}
	sort.Strings(missing)
	loaded := c.loads.DoChan(strings.Join(missing, ","), func() (any, error) {
		found, absent := c.read(missing)
		if len(absent) == 0 {
			return found, nil
		}
		// A cancelled viewer must not cancel a shared lookup for other viewers.
		loadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		fresh := make(map[string]commentAuthor, len(absent))
		for _, id := range absent {
			fresh[id] = commentAuthor{ID: id}
		}
		// Preview authors share Account's existing 100-identity request limit.
		for start := 0; start < len(absent); start += 100 {
			identities, err := resolver.ResolveIdentities(loadCtx, absent[start:min(start+100, len(absent))])
			if err != nil {
				return nil, err
			}
			for _, identity := range identities {
				if _, requested := fresh[identity.ID]; requested {
					fresh[identity.ID] = commentAuthor{ID: identity.ID, DisplayName: identity.DisplayName, AvatarURL: identity.AvatarURL}
				}
			}
		}
		c.mu.Lock()
		now := c.clock()
		if c.entries == nil {
			c.entries = make(map[string]cachedCommentAuthor)
		}
		for id, item := range c.entries {
			if !now.Before(item.expires) {
				delete(c.entries, id)
			}
		}
		for id, profile := range fresh {
			if _, exists := c.entries[id]; !exists && len(c.entries) >= maxCommentAuthors {
				var oldest string
				var expiry time.Time
				for key, item := range c.entries {
					if oldest == "" || item.expires.Before(expiry) {
						oldest, expiry = key, item.expires
					}
				}
				delete(c.entries, oldest)
			}
			c.entries[id] = cachedCommentAuthor{profile: profile, expires: now.Add(commentAuthorTTL)}
			found[id] = profile
		}
		c.mu.Unlock()
		return found, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-loaded:
		if result.Err != nil {
			return profiles, result.Err
		}
		for id, profile := range result.Val.(map[string]commentAuthor) {
			profiles[id] = profile
		}
		return profiles, nil
	}
}
