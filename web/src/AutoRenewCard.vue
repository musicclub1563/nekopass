<script setup lang="ts">
import {computed,ref,onMounted} from 'vue'
import {ElMessage,ElMessageBox} from 'element-plus'
import {api} from './api'
import {cycles,yuan,dateText} from './commerce'
type Settings={enabled:boolean;cycle:string;price_limit_cents:number;status:string;error:string;plan_id:number|null;plan_name:string;prices:Record<string,number>;expires_at:string|null;last_success_at:string|null}
const settings=ref<Settings>(),enabled=ref(false),cycle=ref(''),busy=ref(false)
const options=computed(()=>cycles.filter(c=>c.key!=='onetime'&&settings.value?.prices&&c.key in settings.value.prices))
const price=computed(()=>settings.value?.prices[cycle.value]??0)
const states:Record<string,string>={off:'未开启',waiting:'等待套餐到期',renewed:'上次自动续费成功',insufficient_balance:'余额不足',price_changed:'价格变化，等待确认',unavailable:'套餐或周期已停售',purchase_limit:'已达购买上限',rule_limit:'规则数量超限',already_processed:'该到期周期已处理'}
async function load(){const rows=await api<Settings[]>('profile/auto-renew');settings.value=rows[0];enabled.value=!!settings.value?.enabled;cycle.value=settings.value?.cycle||options.value[0]?.key||''}
onMounted(()=>load().catch(e=>ElMessage.error(e.message)))
async function save(){if(busy.value)return;try{if(enabled.value){if(!options.value.some(o=>o.key===cycle.value))throw Error('请选择在售周期');await ElMessageBox.confirm(`套餐到期时，从余额扣除 ${yuan(price.value)} 元并续费${options.value.find(o=>o.key===cycle.value)?.label}。如价格上涨，将暂停扣款并等待重新确认。`,'确认自动续费',{confirmButtonText:'确认开启',cancelButtonText:'取消'})};busy.value=true;await api('profile/auto-renew','PUT',{enabled:enabled.value,cycle:cycle.value,expected_price_cents:price.value});await load();ElMessage.success('自动续费设置已保存')}catch(e){if(e instanceof Error)ElMessage.error(e.message)}finally{busy.value=false}}
</script>
<template>
 <section class="surface auto-renew-card"><div class="section-header"><strong>余额自动续费</strong></div><div class="section-body" v-if="settings">
  <el-form label-position="top" @submit.prevent="save"><el-form-item label="开启自动续费"><el-switch v-model="enabled" :disabled="!enabled&&(!settings.plan_id||!settings.expires_at||!options.length)" data-testid="auto-renew-enabled"/></el-form-item>
   <p v-if="!settings.expires_at" class="field-tip">当前套餐永不到期或尚未分配，无需自动续费。</p>
   <template v-if="enabled"><el-form-item label="自动续费周期"><el-select v-model="cycle" data-testid="auto-renew-cycle"><el-option v-for="c in options" :key="c.key" :value="c.key" :label="`${c.label} · ${yuan(settings.prices[c.key])} 元`"/></el-select></el-form-item><p class="field-tip">只从余额扣款，不自动充值。到期时续费并重置流量；余额不足会在充值后自动重试。同套餐续费保留个人资源限制。</p></template>
   <p>状态：{{states[settings.status]||settings.status}}<span v-if="settings.error"> · {{settings.error}}</span></p><p v-if="settings.enabled" class="subtle">已确认金额上限：{{yuan(settings.price_limit_cents)}} 元 · 到期时间：{{dateText(settings.expires_at)}}</p>
   <el-button type="primary" native-type="submit" :loading="busy" data-testid="auto-renew-save">保存设置</el-button>
  </el-form>
 </div></section>
</template>
<style scoped>.auto-renew-card{margin-top:20px}.auto-renew-card .el-select{max-width:360px;width:100%}</style>
