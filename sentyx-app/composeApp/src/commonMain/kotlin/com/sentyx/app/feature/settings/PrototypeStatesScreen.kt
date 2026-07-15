package com.sentyx.app.feature.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sentyx.app.core.designsystem.SxChip
import com.sentyx.app.core.designsystem.SxColors
import com.sentyx.app.core.designsystem.SxPrimaryButton
import com.sentyx.app.core.designsystem.SxBackHeader
import com.sentyx.app.core.designsystem.SxSectionHeader

/**
 * Prototype states: developer switchboard whose chips mutate the shared
 * [DemoStateController] flows (device scenario, feed scenario, plan) live across
 * the app, plus a button that fires a simulated urgent event.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun PrototypeStatesScreen(
    vm: PrototypeStatesViewModel,
    onBack: () -> Unit,
    onOpenEvent: (String) -> Unit,
) {
    val state by vm.state.collectAsState()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(start = 22.dp, end = 22.dp, top = 56.dp, bottom = 40.dp),
    ) {
        SxBackHeader("Prototype states", onBack, modifier = Modifier.padding(top = 8.dp))

        Text(
            "Flip these to preview the app under different conditions. Changes apply live across the app.",
            color = SxColors.Muted,
            fontSize = 12.5.sp,
            lineHeight = 18.75.sp,
            modifier = Modifier.padding(top = 10.dp),
        )

        SxSectionHeader("Device state")
        ChipRow {
            vm.deviceScenarios.forEach { scenario ->
                SxChip(scenario.label, selected = state.device == scenario, onClick = { vm.selectDevice(scenario) })
            }
        }

        SxSectionHeader("Event feed state")
        ChipRow {
            vm.feedScenarios.forEach { scenario ->
                SxChip(scenario.label, selected = state.feed == scenario, onClick = { vm.selectFeed(scenario) })
            }
        }

        SxSectionHeader("Plan")
        ChipRow {
            vm.plans.forEach { plan ->
                SxChip(plan.name, selected = state.plan == plan, onClick = { vm.selectPlan(plan) })
            }
        }

        SxSectionHeader("Live events")
        SxPrimaryButton(
            "Simulate incoming urgent event",
            onClick = { vm.triggerUrgent(onOpenEvent) },
        )
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun ChipRow(content: @Composable () -> Unit) {
    FlowRow(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        content()
    }
}
