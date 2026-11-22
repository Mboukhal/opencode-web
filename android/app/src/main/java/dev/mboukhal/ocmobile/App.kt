package dev.mboukhal.ocmobile

import android.app.Application

class App : Application() {
    lateinit var settings: SettingsStore
        private set

    override fun onCreate() {
        super.onCreate()
        settings = SettingsStore(this)
    }

    /** Builds a client from saved settings, or null when not connected. */
    fun client(): OpenCodeClient? = settings.load()?.let { OpenCodeClient(it) }
}
