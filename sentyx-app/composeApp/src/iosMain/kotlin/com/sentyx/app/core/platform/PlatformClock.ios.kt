package com.sentyx.app.core.platform

import platform.Foundation.NSDate
import platform.Foundation.timeIntervalSince1970

actual fun currentEpochMillis(): Long = (NSDate().timeIntervalSince1970 * 1000.0).toLong()
