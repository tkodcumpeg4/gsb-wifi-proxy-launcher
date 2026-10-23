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
        versionCode = 1
        versionName = "0.1.0-faz0"
        // libgsbcore.so is built by ../../build-android.sh (cargo-ndk) into src/main/jniLibs.
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
        jniLibs { useLegacyPackaging = false }
    }
}

kotlin {
    compilerOptions { jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17) }
}
