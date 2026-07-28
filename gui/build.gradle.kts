plugins {
    kotlin("jvm") version "2.0.21" apply false
    id("org.jetbrains.compose") version "1.7.0" apply false
    kotlin("plugin.compose") version "2.0.21" apply false
    kotlin("plugin.serialization") version "2.0.21" apply false
}

allprojects {
    group = "dev.wmux"
    version = "0.1.0"

    repositories {
        google()
        mavenCentral()
        maven("https://maven.pkg.jetbrains.space/public/p/compose/dev")
    }
}
