package main

import (
	"testing"
	"time"
)

func TestNotifyFollowOnlyOnNewEpisodes(t *testing.T) {
	old := favoriteEntry{Name: "示例剧", EpisodeCount: 3, LatestEpisode: "第3集", Checked: time.Now().Add(-time.Hour)}
	cases := []struct {
		name  string
		old   favoriteEntry
		count int
		want  bool
	}{
		{"first check has no baseline", favoriteEntry{Name: "示例剧"}, 3, false},
		{"never checked has no baseline", favoriteEntry{Name: "示例剧", EpisodeCount: 3, LatestEpisode: "第3集"}, 4, false},
		{"same count is not new", old, 3, false},
		{"fewer episodes is not new", old, 2, false},
		{"more episodes is new", old, 4, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			a := &app{notify: func(id, title, body string) { got = append(got, id+"|"+title+"|"+body) }}
			a.notifyFollow(c.old, c.count, "第4集")
			if c.want && (len(got) != 1 || got[0] != "follow:示例剧|示例剧|更新至 第4集") {
				t.Fatalf("notify = %v, want one notification for 示例剧", got)
			}
			if !c.want && len(got) != 0 {
				t.Fatalf("notify = %v, want none", got)
			}
		})
	}
}

func TestFollowingRefreshNotifiesOnlyWhenEpisodesGrow(t *testing.T) {
	a, count, fail := followingFixture(t)
	var got []string
	a.notify = func(id, title, body string) { got = append(got, id+"|"+title+"|"+body) }

	a.refreshFollowing() // First check: 3 episodes, no baseline yet.
	if len(got) != 0 {
		t.Fatalf("first refresh notified: %v", got)
	}
	count.Store(4)
	a.refreshFollowing() // 3 -> 4 episodes.
	if len(got) != 1 || got[0] != "follow:示例剧|示例剧|更新至 第4集" {
		t.Fatalf("growth refresh notified %v, want one notification for 第4集", got)
	}
	fail.Store(true)
	a.refreshFollowing() // Failed refresh keeps the known count.
	if len(got) != 1 {
		t.Fatalf("failed refresh notified: %v", got)
	}
	fail.Store(false)
	count.Store(0)
	a.refreshFollowing() // Empty reply keeps the known count.
	if len(got) != 1 {
		t.Fatalf("empty refresh notified: %v", got)
	}
}

func TestFollowNotificationIDRoundTrip(t *testing.T) {
	name, ok := followNotificationName(followNotificationID("示例剧"))
	if !ok || name != "示例剧" {
		t.Fatalf("notification id round trip = %q, %v", name, ok)
	}
	if _, ok := followNotificationName("other:示例剧"); ok {
		t.Fatal("unrelated notification id parsed as a follow notification")
	}
}

func TestHandleNotificationClickIgnoresUnknownIDs(t *testing.T) {
	a, _, _ := followingFixture(t)
	a.handleNotificationClick("other:示例剧")
	if a.route() != routeHome || a.player != nil {
		t.Fatalf("unknown notification click navigated: route=%v player=%v", a.route(), a.player != nil)
	}
	a.handleNotificationClick("follow:不存在的剧")
	if a.route() != routeHome || a.player != nil {
		t.Fatalf("click for an unfollowed show navigated: route=%v player=%v", a.route(), a.player != nil)
	}
}
