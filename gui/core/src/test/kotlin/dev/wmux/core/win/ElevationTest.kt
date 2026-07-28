package dev.wmux.core.win

import kotlin.test.Test
import kotlin.test.assertFalse

class ElevationTest {

    @Test
    fun `elevation query works without throwing`() {
        // Gradle test workers run unelevated in any sane setup; the real
        // assertion here is that the token query itself succeeds.
        assertFalse(Elevation.isElevated(), "test JVM unexpectedly running elevated")
    }
}
