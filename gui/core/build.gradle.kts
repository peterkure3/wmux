plugins {
    kotlin("jvm")
    kotlin("plugin.serialization")
}

dependencies {
    // Win32 FFI, used only by win/Elevation.kt's UAC "runas" launch now
    // that ConPTY spawning has moved to wmuxd (see WmuxDaemonClient).
    implementation("net.java.dev.jna:jna:5.15.0")
    implementation("net.java.dev.jna:jna-platform:5.15.0")

    // Daemon wire protocol (de)serialization + Flow-based streaming clients.
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-core:1.9.0")

    testImplementation(kotlin("test"))
}

kotlin {
    jvmToolchain(21)
}

tasks.test {
    useJUnitPlatform()
}
