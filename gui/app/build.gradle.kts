import org.jetbrains.compose.desktop.application.dsl.TargetFormat

plugins {
    kotlin("jvm")
    id("org.jetbrains.compose")
    kotlin("plugin.compose")
}

dependencies {
    implementation(project(":core"))
    implementation(project(":cli"))
    implementation(project(":ui"))
    implementation(compose.desktop.currentOs)
}

kotlin {
    jvmToolchain(21)
}

// Diagnostic-only: prints the resolved runtime classpath so the app can be
// launched with plain `java`, bypassing Gradle's JavaExec/daemon process
// wrapping entirely - used to rule out Gradle's own process management
// (likely a Job Object) as a ConPTY confounder. Safe to remove once the
// ConPTY environment investigation is closed out.
tasks.register("printRuntimeClasspath") {
    doLast {
        println(sourceSets.main.get().runtimeClasspath.files.joinToString(";"))
    }
}

compose.desktop {
    application {
        mainClass = "dev.wmux.app.MainKt"

        nativeDistributions {
            targetFormats(TargetFormat.Msi, TargetFormat.Exe)
            packageName = "wmux"
            packageVersion = "0.1.0"
            windows {
                // UAC posture (per spec: elevate only "when such a task
                // requires it"):
                //  - The jpackage-generated launcher exe embeds an asInvoker
                //    manifest by default - wmux never asks for elevation at
                //    launch. Do NOT add a custom highestAvailable/
                //    requireAdministrator manifest.
                //  - perUserInstall keeps the MSI itself from prompting too.
                //  - Individual commands that need admin rights go through
                //    core's Elevation.runElevated() (ShellExecute "runas"),
                //    which shows the standard UAC consent prompt on demand.
                menuGroup = "wmux"
                perUserInstall = true
            }
        }
    }
}
