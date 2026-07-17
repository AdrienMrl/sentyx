plugins {
    // Applied in subprojects; declared here to share the version catalog.
    alias(libs.plugins.androidApplication) apply false
    alias(libs.plugins.kotlinMultiplatform) apply false
    alias(libs.plugins.composeMultiplatform) apply false
    alias(libs.plugins.composeCompiler) apply false
    alias(libs.plugins.kotlinSerialization) apply false
    // On the classpath for :composeApp to apply conditionally (see its build script).
    alias(libs.plugins.googleServices) apply false
}
