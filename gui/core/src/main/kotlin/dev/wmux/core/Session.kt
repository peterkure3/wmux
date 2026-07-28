package dev.wmux.core

/**
 * The client-local split-pane layout tree. Unlike v1's scaffold, this no
 * longer models "a session" (that's wmuxd's dev.wmux.core.net.SessionInfo
 * now, fetched live) — it's purely the on-screen arrangement of which
 * daemon-owned surfaces are open in this window right now, exactly like
 * internal/layout's split tree on the Go side. It is not persisted:
 * wmuxd's state.json persists sessions, not screen layout, and a fresh
 * `wmux` run always starts from a blank arrangement too.
 */
sealed class PaneNode {
    /** A leaf pane attached to one daemon-owned surface, identified by its session ID. */
    data class Leaf(val surfaceId: String) : PaneNode()

    data class Split(
        val orientation: Orientation,
        val ratio: Float, // 0.0-1.0, size of `first`
        val first: PaneNode,
        val second: PaneNode,
    ) : PaneNode()
}

/** All leaves of the tree in depth-first (visual reading) order. */
fun PaneNode.leaves(): List<PaneNode.Leaf> = when (this) {
    is PaneNode.Leaf -> listOf(this)
    is PaneNode.Split -> first.leaves() + second.leaves()
}

/** Removes the leaf for [surfaceId], collapsing its parent split onto the sibling. Null if nothing is left. */
fun PaneNode.without(surfaceId: String): PaneNode? = when (this) {
    is PaneNode.Leaf -> if (this.surfaceId == surfaceId) null else this
    is PaneNode.Split -> {
        val newFirst = first.without(surfaceId)
        val newSecond = second.without(surfaceId)
        when {
            newFirst == null && newSecond == null -> null
            newFirst == null -> newSecond
            newSecond == null -> newFirst
            else -> copy(first = newFirst, second = newSecond)
        }
    }
}

enum class Orientation { HORIZONTAL, VERTICAL }
