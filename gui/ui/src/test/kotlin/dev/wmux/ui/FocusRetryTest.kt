package dev.wmux.ui

import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.onKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.test.ExperimentalTestApi
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performKeyInput
import androidx.compose.ui.test.pressKey
import androidx.compose.ui.test.runDesktopComposeUiTest
import kotlin.test.Test
import kotlin.test.assertTrue

class FocusRetryTest {

    /**
     * Mirrors TerminalPane's exact focus pattern - a LaunchedEffect keyed
     * on `isActive` that retries requestFocus() across a few frames -
     * against the failure mode that motivated it: switching to a new
     * session tears down the old pane's composable and mounts a brand-new
     * one (fresh `remember` scope) in the same frame, which raced a
     * single, unretried requestFocus() call badly enough that typing
     * stopped working after a session switch.
     *
     * `key(instanceKey) { ... }` forces exactly that: a full identity
     * swap, not just a recomposition, matching what WmuxWindow does when
     * activeSessionId changes.
     */
    @OptIn(ExperimentalTestApi::class)
    @Test
    fun `focus retry survives instance swap simulating a session switch`() = runDesktopComposeUiTest {
        var instanceKey by mutableStateOf(0)
        var keyDownCount = 0

        setContent {
            key(instanceKey) {
                val focus = remember { FocusRequester() }
                LaunchedEffect(Unit) {
                    repeat(10) {
                        focus.requestFocus()
                        withFrameNanos {}
                    }
                }
                Box(
                    Modifier
                        .fillMaxSize()
                        .focusRequester(focus)
                        .focusable()
                        .onKeyEvent {
                            if (it.type == KeyEventType.KeyDown) keyDownCount++
                            true
                        },
                )
            }
        }

        waitForIdle()
        onRoot().performKeyInput { pressKey(Key.A) }
        assertTrue(keyDownCount > 0, "expected the first instance to receive key input")

        // Simulate a session switch.
        keyDownCount = 0
        instanceKey = 1
        waitForIdle()

        onRoot().performKeyInput { pressKey(Key.A) }
        assertTrue(keyDownCount > 0, "expected the post-switch instance to receive key input")
    }
}
