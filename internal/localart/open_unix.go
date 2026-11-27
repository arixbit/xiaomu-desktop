//go:build unix

package localart

import "syscall"

// Reject a symlink or avoid waiting for a FIFO writer if the file changes
// between Lstat and OpenFile.
const preventBlocking = syscall.O_NONBLOCK | syscall.O_NOFOLLOW
