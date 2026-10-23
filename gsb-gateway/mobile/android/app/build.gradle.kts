plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.gsb.gateway"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.gsb.gateway"
        minSdk = 26
        targetSdk = 36
        versionCode = 2
        versionName = "0.2.0"
        // libgsbcore.so (Rust core) and libgsbgateway.so (Go bridge, an executable)
        // are built by ../../build-android.sh into src/main/jniLibs.
        ndk { abiFilters += listOf("arm64-v8a", "x86_64") }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            // Phase 0: debug-signed so the APK installs from GitHub; a real
            // release key comes in phase 3.
            signingConfig = signingConfigs.getByName("debug")
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    packaging {
        // Extract native libs to disk: libgsbgateway.so is run as a process.
        jniLibs { useLegacyPackaging = true }
    }
}

kotlin {
    compilerOptions { jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17) }
}
