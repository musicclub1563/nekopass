<script setup lang="ts">
import Icon from './Icon.vue'
import type {EgressOption} from './egress-options'
defineProps<{modelValue?:string;options:EgressOption[];disabled?:boolean;clearable?:boolean;placeholder?:string}>()
const emit=defineEmits<{'update:modelValue':[value:string];clear:[]}>()
</script>
<template>
 <el-select :model-value="modelValue" @update:model-value="emit('update:modelValue',$event)" @clear="emit('clear')" :disabled="disabled" :clearable="clearable" filterable :placeholder="placeholder||'请选择出口节点或节点组'">
  <template #label="{label,value}"><span class="egress-choice" :class="value?.startsWith('group:')?'is-group':'is-node'"><Icon :name="value?.startsWith('group:')?'folder':'node'"/><span>{{label}}</span><small v-if="options.find(o=>o.value===value)?.hint">{{options.find(o=>o.value===value)?.hint}}</small></span></template>
  <el-option v-for="o in options" :key="o.value" :value="o.value" :label="o.name" :aria-label="o.name"><span class="egress-choice" :class="o.kind==='group'?'is-group':'is-node'"><Icon :name="o.kind==='group'?'folder':'node'"/><span>{{o.name}}</span><small v-if="o.hint">{{o.hint}}</small><small>{{o.kind==='group'?'节点组':o.kind==='node'?'单个节点':'直转'}}</small></span></el-option>
 </el-select>
</template>
<style scoped>
.egress-choice{display:inline-flex;align-items:center;gap:8px;width:100%}.egress-choice .ui-icon{flex-shrink:0;width:17px;height:17px}.is-node .ui-icon{color:#47c88a}.is-group .ui-icon{color:#66a9ff}.egress-choice small{margin-left:auto;padding-left:14px;color:var(--el-text-color-secondary)}
</style>
