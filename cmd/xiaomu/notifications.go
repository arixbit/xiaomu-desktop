package main

// System notifications for 追剧 updates.
//
// When the follow-series refresh (launch / manual refresh) finds a new
// episode for a followed show, xiaomu posts one notification per show.
// The notification is the only prompt: the 追剧 page itself keeps the
// new-episode marker, so a denied or dismissed notification loses
// nothing.
//
// Permission is how MyGo does it: the first Show() asks the user and
// waits for the answer, and returns ErrNotificationsDenied when not
// allowed. So xiaomu never asks at launch for no reason — it asks only
// when the first new episode is actually due. A denied answer is
// ignored silently; the in-app marker remains the fallback.

import (
	"strings"

	"github.com/egoist/mygo"
)

// followNotificationPrefix marks notification IDs that belong to a
// followed show. Clicking one opens that show's detail page.
const followNotificationPrefix = "follow:"

func followNotificationID(name string) string {
	return followNotificationPrefix + name
}

func followNotificationName(id string) (string, bool) {
	name, ok := strings.CutPrefix(id, followNotificationPrefix)
	return name, ok && name != ""
}

// postNotification shows one system notification. It is a package-level
// function so tests can substitute it through app.notify.
func postNotification(id, title, body string) {
	// The first Show asks for permission when needed; a denial returns
	// ErrNotificationsDenied. Ignored on purpose: the 追剧 page still
	// shows the new-episode marker.
	_ = mygo.NewNotification(mygo.NotificationOptions{
		ID:    id,
		Title: title,
		Body:  body,
	}).Show()
}

// notifyFollow posts the notification for one show whose refresh found
// new episodes. It fires only when the show had a known episode count
// before (a first check has no baseline to compare against) and the
// count actually grew.
func (a *app) notifyFollow(old favoriteEntry, count int, latest string) {
	if a.notify == nil || old.EpisodeCount <= 0 || old.Checked.IsZero() || count <= old.EpisodeCount {
		return
	}
	body := "更新至 " + latest
	if latest == "" {
		body = "有新集更新"
	}
	a.notify(followNotificationID(old.Name), old.Name, body)
}

// handleNotificationClick opens the followed show a notification was
// about. Notifications clicked after a restart arrive here too, so the
// lookup runs against the saved entries, not live state.
func (a *app) handleNotificationClick(id string) {
	name, ok := followNotificationName(id)
	if !ok {
		return
	}
	for _, e := range a.favorites.entries {
		if e.Name == name {
			a.openFavorite(e)
			return
		}
	}
}
