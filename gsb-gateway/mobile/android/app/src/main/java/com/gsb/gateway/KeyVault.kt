package com.gsb.gateway

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * The 32-byte key the Go process encrypts passwords and the relay token with
 * (secret_android.go). It is stored only wrapped by a non-exportable Android
 * Keystore key and handed to the process in its environment at start.
 */
object KeyVault {
    private const val ALIAS = "gsb_master"
    private const val PREF = "wrapped_data_key"

    fun dataKeyHex(ctx: Context): String {
        val sp = ctx.getSharedPreferences("gsb", Context.MODE_PRIVATE)
        val master = masterKey()
        sp.getString(PREF, null)?.let { wrapped ->
            runCatching { return unwrap(master, wrapped).toHex() }
            // Unreadable (e.g. Keystore reset): start over; saved passwords
            // then need to be entered again.
        }
        val key = ByteArray(32).also { SecureRandom().nextBytes(it) }
        val c = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, master) }
        val blob = c.iv + c.doFinal(key)
        sp.edit().putString(PREF, Base64.encodeToString(blob, Base64.NO_WRAP)).apply()
        return key.toHex()
    }

    private fun unwrap(master: SecretKey, wrapped: String): ByteArray {
        val blob = Base64.decode(wrapped, Base64.NO_WRAP)
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, master, GCMParameterSpec(128, blob, 0, 12))
        return c.doFinal(blob, 12, blob.size - 12)
    }

    private fun masterKey(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return gen.generateKey()
    }

    private fun ByteArray.toHex() = joinToString("") { "%02x".format(it) }
}
