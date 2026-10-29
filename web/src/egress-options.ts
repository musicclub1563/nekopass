import type {RuleNode,RuleEgressGroup} from './api'
export type EgressOption={value:string;name:string;kind:'node'|'group'|'direct';hint?:string}
export const egressValue=(node=0,group=0)=>group?`group:${group}`:`node:${node}`
export function splitEgress(value:string){const [kind,id]=value.split(':');return {egress_node_id:kind==='node'?Number(id)||0:0,egress_group_id:kind==='group'?Number(id)||0:0}}
export function egressChoices(nodes:RuleNode[],groups:RuleEgressGroup[],ingress:number,protocol:string):EgressOption[]{
 const entry=nodes.find(n=>n.id===ingress)
 const multiplier=(n:RuleNode)=>Math.round((Number(entry?.ingress_multiplier??1)+Number(n.egress_multiplier??0))*10000)/10000
 const available=nodes.filter(n=>n.id!==ingress&&n.tunnel_exit_enabled&&n.allowed_ingress_ids.includes(ingress)&&(!n.tunnel_protocol.endsWith('_udp')||protocol==='udp'))
 const ids=new Set(available.map(n=>n.id))
 return [
  ...(entry?.allow_direct?[{value:'node:0',name:'#0 不使用隧道，直接转发',kind:'direct' as const,hint:entry.ingress_multiplier!=null?`${Number(entry.ingress_multiplier)} 倍`:undefined}]:[]),
  ...available.map(n=>({value:egressValue(n.id),name:n.name,kind:'node' as const,hint:n.egress_multiplier!=null?`${multiplier(n)} 倍`:undefined})),
  ...groups.filter(g=>g.node_ids.some(id=>ids.has(id))).map(g=>{
   const peers=available.filter(n=>g.node_ids.includes(n.id))
   const rates=peers.map(multiplier),low=Math.min(...rates),high=Math.max(...rates)
   const hint=peers.every(n=>n.egress_multiplier!=null)?`${low===high?low:`${low}–${high}`} 倍（按实际出口）`:undefined
   return {value:egressValue(0,g.id),name:g.name,kind:'group' as const,hint}
  }),
 ]
}
