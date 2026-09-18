# Agent 完整能力 Goal 进度

启动日期：2026-09-06。用户于 2026-09-07 再次明确必须完成“补齐 + 自测 + 全局 review”，不能在阶段性未通过 review 后停止。本轮查询 Goal 为空，已按此前及本次授权重新建立 active Goal，无额度预算，不将阶段性 review 标记为完成。继续原完整能力范围；下方批次历史保留其当时语义。

## 顺序与边界

2026-09-15范围确认：用户对Computer/PTC/语音实时是否全部纳入本次交付回复“确认”，三项现为必交，不再等待同一范围问题。已在结项清单登记具体验收要求；固定SDK的PTC类、逐工具caller配置及现有统一工具装配入口已读取，尚无生产实现或测试通过。本次确认解除范围阻塞，但不解除真实验收环境限制，不改变user_stop尚未定义的流程语义。Goal此前已由工具标为blocked，自动续跑需在界面恢复；本次不将未完目标标complete或重建为新目标。

2026-09-15恢复更新：用户明确要求“goal继续”，随后系统Goal已核验为active。本次恢复解除此前剩余70%的暂停线；恢复时账户周窗口已用32%、剩余68%，不是新的完成率或预算。不push、不部署、不操作测试机/8860/8880及用户数据、网关、凭据和系统安全策略的边界不变。下面2026-09-14额度规则及G4.217 blocked记录是历史状态，不得据此再次自动暂停本次恢复。

2026-09-14新增用量停止边界：用户要求剩余额度到70%时暂停。按当前账户codex周窗口已用达到30%判定，每批工作前后读取实时usage；到阈值停止开发并通知用户，不标记Goal完成、不擅自兑换重置。首次查询已用20%、剩余80%；账户用量包含其他任务，不能用本任务token计数替代实时额度。

1. 补齐原收敛计划、能力审计和用户基础任务要求的所有功能。
2. 自测、回归与三执行模式的隔离真实验收。
3. 独立全局代码 review，修复问题并重跑相关回归。

实现期间运行针对性测试属于必要自查，不代表提前宣布第二、三阶段完成。保持现有回滚保障及工作区改动；不 push、不部署或操作测试机、不重启或切换用户正在使用的 8860 环境，不擅自变更网关/模型/凭据或使用额度重置。原生 SDK Session/Responses compaction 不得用本地摘要或 recent-N 替代。

范围真源：[原收敛计划](/C:/Users/egois/Desktop/内容生产Agent项目/docs/agent-platform-full-convergence-plan.md)、[能力审计](/C:/Users/egois/Desktop/内容生产Agent项目/docs/agent-platform-capability-audit-20260906.md)、[缺口待办](/C:/Users/egois/Desktop/内容生产Agent项目/docs/agent-platform-capability-rebaseline.md)。审计中“开发暂停”描述其生成时状态，由本文件和本次用户授权更新为开发中，历史发现仍有效。

[原范围结项清单](/C:/Users/egois/Desktop/内容生产Agent项目/docs/agent-platform-completion-checklist.md)固定后续W0-W9逐项核对顺序，覆盖原SDK/UX/GAP编号；批次数量不作为完成比例。

## 当前剩余交付清单（2026-09-14，G4.217）

### G4.222 PTC 检查点重建和取消（2026-09-15）

新增流式SDK测试：父Program及程序子调用完成一次读取后after_turn暂停，经平台_serialize_paused_run与JSON往返，使用新Provider/Agent/模型和RunState.from_json恢复；校验opaque fingerprint完整保留、原工具结果仅一个、begin/complete不增加，并接受匹配父程序的ProgramOutput及最终文本。序列化在内存完成，没有实际磁盘/Go/跨进程重启，不能扩大为真实持久恢复证明。

新增在途读取取消测试，等待工具已进入后取消Runner，验证工具清理、唯一取消审计且无成功回执；所有任务在finally回收。末版PTC专项23通过、0失败/错误/跳过，`.tmp/goal-g4222-ptc-final.xml`，退出0、进程终态。本批无生产代码修改，仅补上述缺失行为证据。

下一项PTC前端可见配置与执行状态、三生产入口同场及真实网关支持；检查当前配置下发和caller审计是否足以让用户区分程序与普通工具结果。Computer和语音实时仍必交，未运行受限Go/stdio/OCI路径、未碰用户服务或数据。全量回归及独立全局review未完成，Goal active。

### G4.221 原生程序caller的Runner隔离验证（2026-09-15）

新增固定SDK Runner测试，以OpenAIResponsesModel子类提供确定性Program/FunctionCall响应；使用SDK正式Program类型及其必需id/call_id/code/fingerprint。验证caller保留至ToolContext、平台begin/start/complete各一次、后续模型输入含工具结果；另验证缺父Program和未获programmatic调用权限均在审计/工具执行前由SDK拒绝。

前两次测试分别18通过/1失败：首次夹具缺父Program，第二次补入不完整字典缺Program必需字段。两者为测试构造不符合固定SDK合同，不修改SDK或绕过校验；保留失败报告`.tmp/goal-g4221-ptc-runner.xml`及`-fixed.xml`。修正正式类型并补反例后末版21通过、0失败/错误/跳过，`.tmp/goal-g4221-ptc-runner-complete.xml`，退出0且进程终态。无真实模型、Go、OCI、stdio或用户环境操作。

本批只补Runner行为证据，不宣称托管JavaScript实际运行、program_output完整交付、取消或RunState恢复已通过。下一步验证程序fingerprint/父子条目在原生检查点与平台序列化中的保留、取消及重建不重放；随后补前端配置/状态。PTC整体、Computer、语音实时、同版全量和最终独立review均未完成，Goal active。

### G4.220 PTC 实际执行模型检查（2026-09-15）

复核三生产入口确认后台/持久流程可使用Chat Completions内容模型，同时有独立Hosted Responses模型。因此G4.218仅检查hosted_model不足。Provider新增execution_model回调，三入口分别绑定当前execution_agent.model或worker._model；每次prepare重新读取，PTC要求实际执行模型为OpenAIResponsesModel，不切换模型或回退网关。未配置PTC保持原模型路径。

新增Hosted模型不能代替执行模型、重准备时模型变化两例；PTC专项18通过、退出0，`.tmp/goal-g4220-ptc-model.xml`，进程终态。仍为配置/装配和已包装工具直接调用测试，没有真实托管程序运行或Go联合验收。固定SDK识别program caller的字段合同已定位，下一步补实际Runner接收该caller、结果与取消/原生RunState重建验证；恢复、用户入口及Computer/语音实时继续必交，完整Goal未完成。

### G4.219 PTC 后端配置与私有目录（2026-09-15）

新增Go ProgrammaticConfig，operator配置显式enabled、workspace_ids和tool_ids，默认关闭。Compile验证只读且无需审批的Runtime工具、去重/范围ID，克隆并规范排序避免调用者篡改；ForWorkspace保留配置并在范围外禁用。私有目录下发与G4.218一致的programmatic_tool_ids，公共目录不暴露该配置。获准Runtime工具绑定配置哈希，沿用既有Begin/Start配置核对，不更改用户运行配置或数据库。现有正式HTTP目录经Store取得工作区Registry，不增加旁路接口。

新增三组Go测试源码覆盖配置拒绝、工作区隔离、复制防篡改、私有下发及默认禁用。agenttool go vet退出0、全后端go build ./...退出0，所有进程终态；未运行Go行为测试，不将编译/静态结果视为其通过。上一批16项Sidecar测试不是本次Go链路运行证据。

下一项必须验证实际三生产Runner模型选择（不能只检查hosted_model即断言主Agent为Responses）、原生programmatic caller执行和检查点恢复、程序结果与取消，以及前端可见配置/状态。PTC目前只有配置到工具装配的源码链路，不能签收实际托管执行；Computer和语音/实时仍待实现，Goal active，完整自测回归及独立全局review未完成。

### G4.218 PTC Sidecar 装配基础（2026-09-15）

用户确认三项范围后Goal已核验active。AgentToolCatalog新增可选programmatic_tool_ids，严格校验唯一字符串列表及read/never的runtime_function描述符；缺字段保持原行为。Provider.prepare在已有审计工具包装后复制指定工具的allowed_callers，并仅在存在实际可用工具时挂载固定SDK ProgrammaticToolCallingTool。要求审计接口与Responses模型，拒绝写入/敏感/审批工具，不修改共享工具或已创建的子任务工具集合。

新增test_programmatic_tools.py，16项通过、退出0，报告`.tmp/goal-g4218-ptc.xml`。使用真实SDK类、配置验证器及直接调用已包装FunctionTool，后端/模型客户端为替身；未执行托管程序、请求真实模型或运行Go/stdio/OCI。这只是配置和装配基础，不是原生PTC执行/取消/恢复的验收。

下一步接Go受信配置/私有目录/工作区可见性与配置哈希，核对三生产Runner的实际Responses模型和恢复合同，再补程序caller/结果交付、用户可见状态及真实兼容门禁。当前Go尚不下发该新增字段，因此生产默认不会启用PTC。Computer及音频/语音/实时仍必须实现；完整回归和独立全局review未完成。不push、不部署或操作用户环境，旧范围阻塞不再沿用。

2026-09-15恢复核对：已读取当前结项清单、SDK能力状态、原双清单/审计及compiler的user_stop拒绝分支。原审计明确Computer/PTC/语音需界定适用性，不能擅自承诺全做；已向用户发出三项是否纳入本次交付的范围问题，未收到答复不视为排除。user_stop语义和真实联合验收条件仍未解决。本次仅核对和更新恢复边界，没有生产代码修改或测试执行，不计实现进展或最终review。原有权限失败未重试、未绕过，完整目标未签收。恢复后阻塞审计重新计数，旧blocked轮次不继承。

阻塞终态补记：G4.217及随后两轮自动续跑连续三次无可推进动作。第二轮只读查询当前命令环境未发现docker/podman，源码仍未装配未决能力；第三轮核对Goal及当前能力状态，未收到范围确认或环境改变。没有正在等待的测试进程，不计verified wait；记录更新不计开发进展。按连续阻塞规则将Goal标为blocked而非complete，停止无效自动续跑。最新账户已用27%、剩余73%，不是70%额度暂停。恢复条件：明确Computer/PTC/语音实时及user_stop的交付语义，或提供符合原授权边界的可用验收环境；不得通过静默删范围、绕过系统限制、操作用户服务或反复安全子集宣称完成。

G4.217：上一轮G4.216修正了权威能力索引，归类为progress。本轮只读复核release-evidence辅助函数、local-gate初始/最终checkpoint及原release matrix；初始写入失败会抛错，末尾通过标记不可达，但磁盘旧报告可能仍为passed，不能作为本次证据。没有确认需要放宽原子替换的代码缺陷，未重跑File.Replace、Go、stdio或真实发布/回滚。

当前不存在足以关闭完整目标的新证据。原matrix仍要求完整Go/pytest、可视化、真实回滚、网关原生压缩、三模式E2E和OCI攻击验收；已知限制未解除。另一方面Computer/PTC/语音实时以及user_stop仍是范围/语义待定，不是已经开发、只欠验收。继续重复静态核对或安全子集不能解决这两类条件。

本轮记录为首次连续无可推进动作的阻塞审计（不是有效实现进展），需要用户明确未决功能交付范围或外部验收条件改变后才能推进相应工作。此前正常开发/新缺陷修复批次不计入连续阻塞；本轮不调用blocked、不标完成。当前额度仍73%，不是触及70%的用量暂停，也不通过消耗额度等待阈值。原环境、数据和授权边界全部保留。

### G4.216 实现与验证记录

G4.216：复核固定 SDK 工具联合类型及生产能力装配，新增 `agent-platform-sdk14-current-status.md`，在原审计顶部和结项清单加入当前状态索引。旧表 Sandbox/Skills/Memory/ToolSearch/CustomTool 未接描述已经过时；目前有原生装配及平台适配，但不代表真实联合通过。Memory 读取 generate=None 与受管两阶段生成分开记录，不误称完全采用 SDK 默认 manager。

PTC、Computer、Voice/Realtime 当前仍未装配；user_stop 仍为编译拒绝的保留条件，不能以普通取消代替。HostedMCP/Responses WS/各存储适配器按替代路径列出，不按未使用类名重复计缺失。官方 PTC 文档及本地 SDK caller 合同已读，未添加模型、网关或新执行环境配置。

本批只更新审计文档并回读，没有新增行为测试，也未执行已知受限路径。最新已用27%、剩余73%，未到用户70%暂停线；Goal active，完整验收和最终独立review未完成。后续应处理原范围尚未签收的具体交付及门禁，不重复已绿色专项、不把能力清单更新作为完成率。

### G4.215 实现与验证记录

G4.215：按SDK-05/11核对子任务as_tool装配、隔离上下文、只读白名单、取消join及Go原执行预算。现有子任务是明确有界只读分析/草稿，不具有独立写入、安装、再委派或原生Memory管理工具；不将其描述成复制主Agent全部权限。本批未确认新增生产缺陷。

test_subagents.py末版18通过、0失败/错误/跳过，3.050秒，`.tmp/goal-g4215-subagents.xml`已回读，进程退出0。真实SDK加确定性模型/后端替身，覆盖并行、取消、恢复、完成回执和权限筛选；未执行Go行为或依赖tmp_path的native_subagent_lifecycle，不能关闭真实联合门禁。

当前已用27%、剩余73%，未到70%暂停线，Goal active。下一项原SDK-14目录完整性与差异核对，明确哪些能力是代码已接但未验、哪些仍未实现/范围待定，不能继续用重复专项取代完整性证明。SDK-14原表仍标未完成；Computer/PTC/音频实时/user_stop不得静默排除。原范围同版完整回归、真实联合、权限/网关/OCI阻塞及最终独立全局review仍未关闭。

### G4.214 实现与验证记录

G4.214：继续未知外部副作用与暂停恢复，阅读tool_outcomes、实际SDK未知结果测试及三模式生产测试前提。确认原生生成条目保留已发出的两个操作，恢复追加用户核对事实不自动重放；只有结构化特定事务拒绝才按可延期提交处理。本批未确认新增代码缺陷，不修改既有恢复语义。

external_tool_recovery/native_pause_checkpoint/audited_mcp_recovery安全子集21通过、0失败/错误/跳过，`.tmp/goal-g4214-outcome-recovery.xml`已回读，进程退出0。明确deselected三模式生产恢复18项（tmp_path前提受此前权限限制），native_pause_semantics磁盘测试也未执行。进程内MCP替身没有启动stdio；不可将该子集称为三模式生产恢复、Go收件或真实外部写入验收。

当前已用27%、剩余73%，未到70%暂停线，Goal active。原范围真实联合/旧状态恢复、同版完整跨层回归、W9权限与网关以及最终独立全局review仍未关闭。后续不重复这条已检查且未定位新缺陷的恢复路径，继续原要求的未验交付与范围核对；不能以更多安全子集替代受阻门禁。

### G4.213 实现与验证记录

G4.213：继续Hosted/普通产物交付合同，阅读tool_outputs、hosted_tools、Go StoreAgentToolOutput/OpenAssetContent及下载handler。确认未知扩展名有意封装ZIP，不以原输入字节大小/文件名直接约束最终资产；下载复核作品与源文件可用性，工具输出复用项目上传/配额/当前调用状态。本批未确认新增生产代码缺陷，没有为纯校验数量改动既有合同。

Hosted tools、Hosted inputs、artifact delivery三文件52通过，0失败/错误/跳过，9.175秒，`.tmp/goal-g4213-delivery.xml`已回读，进程1942退出0。证据为真实SDK加模型/服务/内容读取替身，包括审批、输入绑定、输出保存、错回执与失败不报成功；未调用真实提供方、未执行stdio/Go行为、未运行实际下载接口。此前末版前端1067通过仍是组件证据。

当前已用27%、剩余73%，未到70%暂停线，Goal active。后续按原范围核对尚缺证据的执行恢复与最终验收条件，不因工具交付专项通过宣称SDK核心全面达标。原W9权限/网关、真实OCI/Go/用户联合、旧状态恢复、完整同版回归及最终独立review仍未关闭。

### G4.212 实现与验证记录

G4.212：文件交付源码核对确认后端按不可变版本提供下载，文本优先content_markdown是已有显式合同，不擅自更改。前端ArtifactDownloads只核对ID，损坏downloads/warnings等字段可能先进入delivery状态再渲染报错；新增状态写入前的格式列表、重复format、文件名、警告、标题和版本检查，失败保留重新读取入口，不创建下载或采用回执中的外部URL。新增10个错误元数据/恢复用例，未运行修复前反例，不声称已有崩溃执行证据。

专项通过后运行末版完整前端：72文件1067通过、0失败/错误/跳过，`.tmp/goal-g4212-frontend-all.xml`已回读，进程46506退出0；TypeScript build进程58369退出0。报告time=70.1762608为报告字段，不当作完整进程墙钟耗时。覆盖本轮身份/Skill安装/下载修改及原组件回归，不是Go、真实浏览器或外部提供方联调验收。

当前已用27%、剩余73%，未到70%暂停线。未改用户数据、服务或网关；原范围同版跨层完整回归、真实联合、W9权限/网关阻塞、旧状态恢复与最终独立全局review仍保留，Goal active。下一步继续后端/SDK交付合同的未验部分，不重复前端全量直到有相关源码变化。

### G4.211 实现与验证记录

G4.211：继续作者Skill安装后的即时调用校验，确认project_skills.install_workspace_skill未复核目录读取结果的execution_mode。原安装回执inline，但目录返回background_task或缺失模式时仍标ready_in_current_turn=true，两个新增反例修复前失败（`.tmp/goal-g4211-readiness-before.xml`）。现在仅目录明确inline且原ID/version/hash/scope匹配时激活；不改已提交安装成功事实，不重发安装，未匹配返回CAPABILITY_VERSION_NOT_VISIBLE。

同时核对Go PublicSkill真实合同不含scope_ref，未凭测试夹具假设增加不存在字段要求。ProjectSkill文件安全子集31通过，0失败/错误/跳过，3个含tmp_path/stdio的后台二次重启用例明确deselected，不能声称全文件通过。`.tmp/goal-g4211-readiness-after.xml`已回读，2.611秒，进程退出0。三模式多文件作者真实磁盘/Go/模型联合仍未验，不运行已知受阻路径。

当前已用26%、剩余74%，未到70%暂停线；本批未操作用户数据、服务或网关，Goal active。原范围同版完整回归、真实联合、W9权限/网关阻塞与最终独立全局review仍保留。后续继续交付合同及未核实的原要求，不将两项修复作为完整作者/SDK功能结项。

### G4.210 实现与验证记录

G4.210：回到对话Skill作者交付与文件面板，定位安装范围的权限变化问题：先选择workspace、角色随后降为editor时，下拉项已移除workspace但内部选择未切换，确认安装一直禁用。反例修复前失败（`.tmp/goal-g4210-skill-scope-before.xml`），SkillDraftPanel改为未提交选择按当前授权范围取有效项；结果未知的pending命令优先使用原scope，不自动改成新范围或新请求。

新增两个测试：降权前未提交的选择可继续project安装；未知workspace提交降权后禁止重试，恢复admin后仍用完全相同参数/幂等键重试。ProjectFiles与SkillDraftPanel末版46通过、0失败/错误/跳过，`.tmp/goal-g4210-skill-scope-final.xml`已回读，TypeScript build退出0，全部进程终态。此为前端组件/API替身证据，不是Go安装或实际用户Skill调用验收。

当前已用26%、剩余74%，未到70%暂停线。上一批W9报告原子替换与stdio权限限制仍保留，不重跑或绕过；未动用户服务、数据库或网关。下一步继续原作者多文件发布/安装/调用的SDK与后端合同核对，保持W0-W9与最终独立review未完成状态，Goal active。

### G4.209 实现与验证记录

G4.209：W9发布证据当前验证出现环境阻塞，不能沿用历史51通过。本轮仅运行发布证据辅助脚本，51项中48通过、3失败，`.tmp/goal-g4209-release-evidence.json`已回读，进程退出1。原子File.Replace拒绝访问；两复制入口的预检查未到预期缺工具链错误，实际三个latest夹具仍为old/passed，故不能视为失效成功回执已被替换。两入口是否同一底层拒绝尚无直接异常证据，不归因成已确认代码缺陷。不运行真实发布/回滚，不降低原子替换、不改权限策略或换路径重试。

另运行不带live、不加载生产env的兼容探针，`.tmp/goal-g4209-sdk-compatibility.json`已回读：固定openai-agents 0.21.1/openai 3.3.1，8个接口/适配存在检查supported，stdio调用1项PermissionError blocked。探针退出0不代表门禁通过；真实gate现有实现会拒绝任何非supported状态。此入口内含stdio子进程，仍触及此前已知受限路径，后续不再次运行此完整探针，接口存在不作为真实工具或网关证据。未请求模型或网关。

本批未改生产源码。当前已用26%、剩余74%，未到70%暂停线，Goal保持active；此前G4.207/208已产生新测试/同版专项证据，本批新增当前W9失败证据，未宣称整体完成。W0-W9完整真实验收、已知环境/网关限制、旧状态联合恢复与最终独立全局review仍未关闭。后续优先检查原范围缺少验证的交付合同，不反复运行已知拒绝的Go/stdio/File.Replace/OCI路径。

### G4.208 实现与验证记录

G4.208：核对W8归档解析/恢复、文件操作、容器内进程静止/释放、Runtime恢复原回执与清理调度源码。本批未确认新增生产缺陷，不为已有环境阻塞改写执行路径。文件恢复只接受空目录或完全相同内容；重新恢复要求明确版本/hash/request，未知进行中请求不自动重放；这些为源码结论，不是容器行为证明。

test_native_workspace_transport/test_native_workspace_tools/test_native_session三文件64通过、0失败/错误/跳过，8.740秒，`.tmp/goal-g4208-workspace.xml`已回读，进程退出0。含真实SDK对象和随机loopback HTTP夹具，后端/模型/沙箱为替身；未操作8860/8880。原tmp_path文件辅助测试已知权限阻塞，本批未重新运行或换目录绕过；Go行为、真实OCI隔离/配额/网络/进程及Windows/Linux联合攻击验收仍未通过。

当前已用26%、剩余74%，未到70%暂停线，Goal保持active。下一组W9网关、观测、发布/回滚与原范围同版验收证据；不重复静态重查本批无新增缺陷的W8路径。旧状态联合恢复、磁盘权限/保留/跨进程、Go与真实联合及最终全局review仍未完成。

### G4.207 实现与验证记录

G4.207：继续W7身份隔离，阅读HTTP当前principal复核、执行身份绑定、Memory用户管理/生成控制及列表所有者过滤，以及前端弹窗按workspace/user/role重建的实际接线。本轮未定位新增生产缺陷。补四个前端行为用例：旧保存等待期间切换用户、工作区、角色，旧回执不能覆盖新正文或触发新身份刷新；切换用户时旧读取abort，忽略仍迟到的正文。角色降级仍只读。

ProjectMemory文件12通过、0失败/错误/跳过，`.tmp/goal-g4207-memory-identity.xml`已回读；TypeScript build退出0，所有进程终态。未更改生产源码、用户数据库、服务或网关。这是组件/API替身验证，不是Cookie切换、跨租户数据库行为或撤权联合验收，W7不签署整体完成。

当前已用26%、剩余74%，未到70%暂停线。旧状态联合恢复、实际磁盘权限/保留/跨进程、Go行为/真实联合、原范围同版完整验收与最终全局review仍未完成，Goal保持active。下一核对W8受限工作区与脚本的原范围未验条件，不因外部阻塞重复已查Memory接线；禁止安装OCI或绕过Application Control。

### G4.206 实现与验证记录

G4.206：回到原范围执行生命周期，静态核对主执行SDK结果归档调用、流式断开清理和Session测试夹具。本批未发现并确认新的生产代码缺陷，未修改生产代码。归档仍在SDK运行返回后、主执行返回前等待，设置有超时；本批不将既有专项测试解释为真实网络断开与归档同时发生的联合证明。

四文件test_stream_shutdown/test_session/test_background_worker/test_memory_autocapture回归46通过、0失败/错误/跳过，耗时5.104秒，`.tmp/goal-g4206-lifecycle.xml`已回读，进程退出0。Session使用测试目录随机独立数据库并由原夹具清理；未操作用户数据库、服务或网关。覆盖已有断开保留执行锁、Session持久化与原生压缩协议合同、后台取消及捕获合同，不是实际网关原生压缩验收。

旧状态联合恢复、实际磁盘权限/保留/跨进程、Go行为/真实联合、原范围同版完整验收和最终全局review仍未完成。批前后实时账户已用26%、剩余74%，未到70%暂停线，Goal保持active。继续原范围核查；达到阈值暂停，不标记完成。

### G4.205 实现与验证记录

G4.205：私有队列初始化不再只依赖表名/CREATE IF NOT EXISTS，核对列顺序/类型/NOT NULL/主键，并拒绝队列表自定义trigger，避免同名不兼容数据库启动后持续失败或删除行为被改写。工厂失败关闭连接、错误脱敏，不自动迁移未知结构。新增缺列/错类型/可空/触发器四反例，既有新库/恢复/捕获合同保留。

五文件64通过、0失败/错误/跳过，`.tmp/goal-g4205-archive-schema.xml`已回读，进程退出0，全部使用内存SQLite或工厂替身；未打开或修改实际数据库。本批不是磁盘权限/跨进程验收。

旧状态联合恢复、实际磁盘/保留/跨进程、Go行为/真实联合、原范围同版完整验收和最终全局review仍未完成。当前剩余75%，未到70%停止线，Goal保持active；下一步继续按原范围跨模块收敛，不将队列专项替代完整SDK/用户链路验收。

### G4.204 实现与验证记录

G4.204：核实最终ResolveAgentToolApproval同时复核Memory工具候选与发布候选，不能只靠前端按钮状态。Go测试源码加入错误阶段和取消生成的真实批准API拒绝断言；恢复测试夹具状态后原approval version/subject不变，继续原发布路径。runtime/httpapi/agenttool vet退出0，未运行Go行为测试。

现有六文件Memory前端组件回归59项通过、0失败/错误/跳过，`.tmp/goal-g4204-memory-ui.xml`已回读，进程退出0；覆盖候选审批、工具审批、生成任务、授权、来源选择及私有记忆管理。此为组件/API替身，不是浏览器或真实Go联调；本批未改前端源码，不能把专项通过称作真实用户审批验收。

旧状态联合恢复、实际磁盘/保留/跨进程、Go行为/真实联合、原范围同版完整验收和最终全局review仍未完成。当前剩余75%，未到70%停止线，Goal保持active，未操作真实用户数据或服务。

### G4.203 实现与验证记录

G4.203：发现跨模块接线缺陷：生成任务saveMemoryPublicationIntentTx已按memory:<id>:consolidation保存工作区，但memoryProposalTx仍按memory:<id>查询，导致有效生成候选被判Available=false、无法批准。预览改为先复用memoryToolProposalTx校验当前本人、原生成任务状态/来源/参数，再核对原工具consolidation阶段并查询确切阶段工作区；普通对话/后台/持久流程原键保持。错误阶段不暴露Files、不允许批准，但保持可拒绝。

在原生成发布/完成恢复Go测试链增加批准前候选可用断言及错误阶段反例，然后继续原批准/发布流程。Go全量build及runtime/httpapi/agenttool vet均退出0，Go行为未运行；此为已定位并修源码，不是前端真实可用或完整发布验收证明。未操作用户数据/服务。

旧状态联合恢复、实际磁盘/保留/跨进程、Go行为/真实联合、原范围同版完整验收和最终全局review仍未完成。当前剩余75%，未到70%停止线，Goal保持active。

### G4.202 实现与验证记录

G4.202：旧workspace阶段绑定更新与Memory控制最终状态更新复用updateExactlyOne，零行更新明确失败，防止数据库忽略其中一步后提交部分恢复。扩充Go测试源码：目标阶段已存在、快照版本前进、绑定写入被忽略、任务状态写入报错或被忽略；失败须保留paused/status revision/checkpoint和旧activity_key、holder/generation。测试隔离数据库事务，不是SDK联合恢复。

末版Go全量build、runtime/httpapi/agenttool vet均退出0，遵守限制未运行Go行为测试，不将新增反例写成已验证通过。本批未更改真实数据、环境、凭据或服务。旧状态联合验收、实际磁盘/保留/跨进程、Go行为及真实联合、原范围完整验收和最终全局review仍未完成；当前剩余75%，未到70%停止线，Goal保持active。

### G4.201 实现与验证记录

G4.201：新增旧无phase Memory工作区的显式owner resume事务适配，不在普通只读租约查询中迁移。原checkpoint必须通过完整hash及schema、phase/owner/source/base/model绑定、state hash和原workspace snapshot版本/正文完整性核对；旧租约需到期，目标phase工作区不能已存在。整合还须原冻结input plan。仅改activity_key阶段绑定，保留session、snapshot、checkpoint、holder、epoch和generation，后续仍走原新token/确切generation的fenced takeover。不清空SDK状态或默认重跑，缺失证据继续拒绝。

Go全量build与runtime/httpapi/agenttool vet均退出0。新增测试源码覆盖提取阶段显式恢复、错phase/source/snapshot及活跃旧租约拒绝，并断言原checkpoint与holder/generation保留；其SDK state为隔离引用验证的夹具，不是SDK恢复执行证明。遵守限制未运行Go行为测试，整合与真实旧状态的联合验收未完成。

旧记录缺失checkpoint/input plan时仍无法证明安全恢复，适用数据待核实；实际磁盘/保留/跨进程、Go行为/真实联合、原范围同版完整验收和最终全局review仍未完成。当前剩余75%，未到70%停止线；未修改真实用户数据/服务，Goal保持active。

### G4.200 实现与验证记录

G4.200：复现单个损坏归档阻塞整批，文本损坏抛完整性错误、BLOB类型损坏抛AttributeError，两个反例失败（`.tmp/goal-g4200-corrupt-before.xml`）。队列读取增加存储类型检查，默认严格读取继续拒绝；恢复worker显式选择跳过损坏项，仍完整验证有效项，仅日志记录失败数量，不导出/修补/擅自删除损坏正文。损坏数据继续占用原配额并保留至正常过期或获授权处理，不称已恢复。

六文件70通过、0失败/错误/跳过，`.tmp/goal-g4200-corrupt-after.xml`已回读，进程退出0；含真实内存SQLite损坏与有效条目并存、仅正确条目发送及日志不含正文。真实磁盘/后端不在证据范围。原范围各项未按批次数计完成率。

旧工作区恢复合同与数据适用性、实际磁盘/保留/跨进程验证、Go行为/真实联合、同版完整验收和最终全局review仍未完成。当前剩余75%，未到70%停止线，Goal保持active，未操作用户数据/环境/服务。

### G4.199 实现与验证记录

G4.199：Memory worker工厂在创建客户端前核对model ID长度、lease/poll整数类型及范围，避免客户端已创建后才被worker构造器拒绝。保留实际model/原SDK执行逻辑，不改模型配置。新增反例通过禁止客户端构造验证前置门禁。四文件46通过、0失败/错误/跳过，`.tmp/goal-g4199-worker-preflight.xml`已回读，进程退出0；不是模型客户端任意构造失败的完整清理证明，未访问网关或真实服务。

旧工作区恢复合同与适用数据核实、实际磁盘/跨进程验证、Go行为/真实联合、原范围同版全量和最终全局review仍未完成。当前剩余75%，未到70%停止线，Goal保持active。

### G4.198 实现与验证记录

G4.198：SDKMemoryWorker及MemoryArchiveWorker工厂在创建后端/模型客户端前校验内部服务凭据，空值、纯空白或非字符串明确失败，不再让无凭据worker启动后持续无效请求。凭据不写入错误，不改变已有有效token内容。六文件73通过、0失败/错误/跳过，`.tmp/goal-g4198-worker-credentials.xml`已回读、进程退出0，含工厂反例和应用/配置/生命周期回归；未修改实际凭据或配置。

本轮git ls-files确认native_workspace.go、agent_memory_generation.go、memory_worker.py尚未跟踪，但这不能证明任何已运行环境不存在旧记录。旧无phase兼容风险保持待核实，不把测试构造的旧记录说成用户已存在的数据；不能盲迁移阶段或清空checkpoint。实际磁盘/跨进程、Go行为/真实联合、原范围同版全量和最终全局review仍未完成。当前剩余75%，未到70%停止线，Goal保持active。

### G4.197 实现与验证记录

G4.197：Memory生成开关改为明确布尔校验，保留原true/1/yes/on及false/0/no/off/空值与大小写空白语义，拼错或未知值不再静默关闭worker而是脱敏报错。补生成/归档开关独立性、默认关闭、合法值和拼写反例。五文件51通过、0失败/错误/跳过，`.tmp/goal-g4197-memory-config.xml`已回读，进程退出0；环境变量修改仅限monkeypatch测试进程，未修改真实配置/服务。

原范围仍未全部验收：旧工作区恢复合同、实际磁盘权限/保留/跨进程验证、Go行为与真实联合、同版全量及最终全局review待完成。当前剩余75%，未到70%停止线，Goal保持active。

### G4.196 实现与验证记录

G4.196：补sidecar README私有Memory运行/回滚说明，按当前config/app/queue/worker源码明确两个独立开关、默认关闭、用户同意不等于启动worker、无专用队列不等于禁用普通归档、当前Go版本/接口要求、专用目录/ACL责任、原数据与24小时在线清理边界、错误重试分类、停机关闭顺序及禁用不等于取消/删除。没有把healthz或SQLite页清理当作真实验收/完整擦除；说明旧无phase工作区仍拒绝且不允许清空checkpoint伪造恢复。本批文档补齐，不新增测试通过数，不修改实际配置或部署。

旧工作区恢复合同、实际磁盘权限/保留/跨进程验证、Go行为/真实联合、原范围全量门禁和最终全局review仍未完成。当前剩余76%，未到70%停止线，Goal保持active。

### G4.195 实现与验证记录

G4.195：对当前所有test_memory*.py文件扩大回归，明确排除已知tmp_path权限阻塞的test_memory_publication.py，未改临时目录/安全设置规避限制。其余35文件447项通过、0失败/错误/跳过，`.tmp/goal-g4195-memory-regression.xml`已回读，进程退出0。覆盖SDK冻结来源、私有工具/审批、提取/整合、阶段准备、checkpoint/暂停恢复、来源传输与新归档队列/调度/生命周期。排除文件仍未验，不因报告skipped=0称完整Memory或SDK全量通过；真实Go、OCI、网关和磁盘联合不在该报告范围。

本批扩大同版回归，没有新增生产行为；旧无phase工作区恢复合同、实际磁盘权限/保留/跨进程验证、Go行为/真实联合、原范围全量门禁和最终全局review仍未完成。当前剩余76%，未到70%停止线；未操作用户数据/环境/服务，Goal保持active。

### G4.194 实现与验证记录

G4.194：增加恢复并发与实际异步deadline测试：两个独立worker在同进程同时读取同一SQLite条目，等待两请求均进入后释放回执，原payload完全一致、重复ack无错误且最终队列为空；一秒请求超时取消当前异步等待，保留原条目，随后成功重试。五文件58通过、0失败/错误/跳过，`.tmp/goal-g4194-recovery-concurrency.xml`已回读，进程退出0。使用真实内存SQLite与SDK冻结结果，但后端是替身；不是实际跨进程锁、线程内HTTP终止或Go幂等事务证明。本批不新增生产行为。

旧无phase工作区重读：nativeWorkspaceOwnerTx当前拒绝无阶段记录，CurrentNativeWorkspaceLease/Validate查询是回滚只读事务；不能在那里简单重命名activity_key当作已迁移。旧提取快照也不能默认属于当前整合阶段。需要独立、可验证阶段和原快照/工具副作用的恢复合同，当前保持拒绝保护，该项未完成而非已兼容。用户resume要求started任务具备checkpoint，不能用清空checkpoint重跑来冒充恢复。

实际磁盘权限/日志保留/跨进程竞争、旧工作区恢复、Go行为及真实联合、原范围同版完整验收和最终全局review仍未完成。当前剩余76%，未到70%停止线；未操作真实数据/服务，Goal保持active。

### G4.193 实现与验证记录

G4.193：复现恢复批次缓存越过保留期限/确认删除继续发送的问题：前一项网络等待期间后一项已过期或被其他路径ack，旧worker仍将其发送，两个反例均失败（`.tmp/goal-g4193-expiry-before.xml`）。新增queue.is_pending，以原key+hash查询并先清理过期项；worker逐项发请求前复核，不再仅依赖批次开始时的缓存。已发出的请求不能据此撤回，仍依靠后端当前来源/授权与幂等检查；未声称消除跨进程检查至发送间的竞态。

六文件63通过、0失败/错误/跳过，3.996秒，`.tmp/goal-g4193-expiry-after.xml`已回读，进程退出0。真实内存SQLite、SDK冻结结果和确定性时钟/传输替身覆盖过期、其他路径ack及原hash匹配；不是实际磁盘/跨进程验收。

旧无phase工作区当前明确拒绝，兼容恢复方案仍待处理；实际磁盘权限、日志/备份保留、跨进程竞争、Go行为和真实联合、原范围同版完整验收和最终全局review保持未完成。当前剩余76%，未到70%停止线；未操作真实数据或服务，Goal保持active。

### G4.192 实现与验证记录

G4.192：私有归档队列初始化强制PRAGMA secure_delete=ON并验证返回值，防止确认/过期删除后原payload仍留在SQLite可重用页。先显式关闭该设置复现两项失败（`.tmp/goal-g4192-purge-before.xml`），修复后真实内存SQLite serialize逐字节检验原存储payload不再残留，两路径通过。此证据仅覆盖当前数据库映像，不代表历史journal/WAL、备份或存储设备安全擦除。

工厂除路径解析同址检查外增加samefile身份检查，拒绝硬链接指向SDK Session；身份查询OSError脱敏拒绝，不继续打开数据库。别名测试模拟文件身份，并非实际创建硬链接或磁盘验收。五文件中间52通过；最终八文件77通过、0失败/错误/跳过，6.017秒，`.tmp/goal-g4192-archive-privacy.xml`已回读，进程退出0。无实际用户数据/数据库/配置或服务变更。

实际磁盘私密权限、日志/备份保留语义、跨进程恢复及竞争、旧工作区、Go行为/真实联合、原范围同版完整验收和最终全局review仍未完成。当前剩余76%，未到70%停止线，Goal保持active。

### G4.191 实现与验证记录

G4.191：恢复授权的原来源查询增加精确缺失分类：原turn/项目关联/原snapshot查询sql.ErrNoRows、原background/stateful尝试的两个明确NOT_FOUND映射SOURCE_REVOKED，供现有worker删除失效待归档项。仅作用于来源查询，不能把归档写入或其他数据库错误误判成撤销；未知NOT_FOUND、数据损坏、数据库忙和取消原样保留。

新增Go测试代码覆盖三模式已完成来源的无旧token恢复、重复恢复、未知原来源、作品删除，以及缺失/未知错误分类。测试用SQL构造终态以隔离恢复授权，不是三模式实际执行完成验收。Go全量build与runtime/httpapi/agenttool vet均退出0；遵守限制没有运行Go行为测试，本批没有新的Python通过数。

仍需队列实际磁盘/跨进程恢复、保留/删除语义与生命周期竞争验收；旧工作区、Go行为及真实联合、原范围同版完整验收和最终全局review未完成。当前剩余76%，未到70%停止线，Goal保持active，未操作真实数据或服务。

### G4.190 实现与验证记录

G4.190：新增MemoryArchiveWorker并接应用lifespan，专用队列配置存在时启动，不依赖Memory生成开关。独立服务重新授权归档，每轮最多16项、轮转覆盖最多256项队列，避免前排未完成来源长期阻塞后排。单次请求有界，失败只记录类型；NOT_READY、未知错误、服务认证失败与未知409保留，只有明确来源撤销/Memory授权冲突/用户权限失效/来源身份不符且403/409/410才清理。成功精确回执后ack，ack失败不报已删除；取消保留待确认项。应用退出先停止所有worker再关闭队列，启动失败也清理。

五文件初验47通过（`.tmp/goal-g4190-archive-worker.xml`，5.650秒）；十文件扩大回归136通过、0失败/错误/跳过（`.tmp/goal-g4190-archive-regression.xml`，8.500秒），均回读JUnit、进程退出0。覆盖真实内存SQLite与已完成SDK结果、未知响应原数据重试、明确撤销与服务认证区分、停机/重复停止、轮转、ack失败、生命周期、应用接口和既有捕获合同。传输/生命周期使用替身，不是实际后端或磁盘/跨进程验证；未开启配置或启动服务。

原来源不存在时的错误分类与及时清理、队列实际磁盘/进程恢复及生命周期竞争、旧工作区、Go行为与真实联合、同版完整验收、最终全局review仍未完成。当前剩余76%，未到70%停止线，Goal保持active；未修改真实用户数据/环境或操作测试机。

### G4.189 实现与验证记录

G4.189：补后端 RecoverAgentMemoryArchive 及独立服务 archive-recover HTTP 入口，拒绝委托执行上下文/执行头，不保存或复用旧 attempt token。共享原归档事务，重新核对原来源当前成功状态、持久流程 current_attempt_id、当前用户权限、原 Memory snapshot 与当前版本、原 consent revision、幂等写入和自动生成授权。恢复仅接受已完成且无待处理中断的冻结 SDK 结果。NOT_READY 与 SOURCE_REVOKED 均映射409但保留不同code，后续调度必须区分暂未完成和明确撤销。

sidecar增加独立服务传输与队列payload校验，拒绝任何当前Agent执行上下文；原冻结正文和授权revision原样发送，精确验证回执，服务端错误码保持，正文与旧token不进入错误。三文件Python67通过、0失败/错误/跳过，9.598秒，`.tmp/goal-g4189-archive-recovery.xml`已回读。覆盖三来源无旧执行头、重复发送、非法授权/额外字段、回执不匹配和暂未完成/撤销传输；HTTP是替身，不是实际Go联合验收。

Go全量build及runtime/httpapi/agenttool vet最终均退出0。新增Go授权/未完成来源、主对话成功与幂等、跨用户/旧授权/失败SDK结果/forget、HTTP拒绝与状态码测试代码；遵守限制未执行Go行为测试，不能将其视为通过。

恢复后台调度及lifespan接入仍未完成；还需审查来源不存在的错误分类、三模式成功和生命周期竞争、独立队列的实际磁盘/进程恢复与清理。旧工作区、Go行为及真实联合、原范围同版完整验收和最终全局review仍未完成。未修改真实用户数据或环境、未启动/重启服务。当前剩余76%，未到70%停止线，Goal保持active。

### G4.188 实现与验证记录

G4.188：应用工厂增加CONTENT_AGENT_SIDECAR_MEMORY_ARCHIVE_DB_PATH，默认空关闭；要求独立绝对文件路径，拒绝:memory:、相对路径、直接符号链接、Session路径同址及含无关表的数据库。打开失败关闭连接并脱敏报错，不自动建目录。lifespan创建一次队列，注入应用创建的主对话runtime和两类任务worker，退出先等待worker停止再关闭队列；启动失败也清理。配置该路径时拒绝外部注入runtime，以免静默绕过应用队列所有权。

六文件Python51通过、0失败/错误/跳过，5.253秒，`.tmp/goal-g4188-archive-factory.xml`已回读。工厂测试以真实内存SQLite替代文件connect，生命周期使用worker/queue替身；并非实际磁盘或并发进程验收。未改变环境变量、未打开实际队列数据库或重启服务。应用注入已落代码，重新授权恢复调度仍未接，因此不能称跨进程归档完整。

当前额度剩余77%，未到70%停止线。后端恢复授权、实际磁盘/进程恢复、旧工作区、Go行为和真实联合、同版完整验收及最终全局review仍未完成，Goal保持active。

### G4.187 实现与验证记录

G4.187：OpenAIAgentsRuntime、SDKBackgroundTaskWorker、SDKTaskWorker构造器支持可注入archive_queue；主对话新建/恢复context、后台context和StatefulExecution准备后都由当前进程依赖赋值，不从旧checkpoint恢复队列对象。无注入时保持原默认None行为，未自动打开数据库。

五文件Python107通过、0失败/错误/跳过，`.tmp/goal-g4187-archive-injection.xml`已回读。后台实际SDK路径断言原队列对象进入context；持久流程准备使用替身StatefulExecution验证依赖注入；三模式capture/原运行工具专项保持。不是应用工厂或磁盘恢复验收。

应用工厂的专用存储配置与生命周期、后台重新授权恢复合同仍待接；跨进程归档尚未完成。旧工作区、Go行为/真实联合、同版完整验收和最终全局review未完成。当前额度剩余77%，未到70%停止线；未操作实际服务/用户数据库，Goal保持active。

### G4.186 实现与验证记录

G4.186：共享生产capture_completed_memory接可注入的私有队列，AgentContext增加非repr队列引用；队列存在时原分段先put再发archive，确认写入/原回执后ack，明确拒绝时清理队列项及内存引用。ack失败保留未确认状态及待恢复项，不报已删除；拒绝后的持久清理失败仅释放本组内存并记录脱敏错误，不宣称数据库已擦除。

初始三类容量反例失败：put抛BackendError被误归为远端丢回执。已增加archive_attempted条件，未尝试archive的本地入队失败不回读、不降级绕过队列发送。五文件Python102通过、0失败/错误/跳过，8.117秒，`.tmp/goal-g4186-capture-queue.xml`已回读，进程退出0。含三执行来源+SQLite内存队列、成功/未知/拒绝/容量/确认删除失败及既有checkpoint与传输合同，不是实际磁盘或进程重启验收。

应用工厂尚未建立/注入队列，后台重新授权恢复合同未接；因此现有服务不会自动启用该队列，不称跨进程归档完成。下一步仍须明确专用存储配置/生命周期及不持久旧执行凭据的后端恢复授权。旧工作区、Go行为/真实联合、同版完整验收及最终全局review未完成。

实时额度剩余77%，未到70%停止线。未操作实际服务/用户数据库，Goal保持active。

### G4.185 实现与验证记录

G4.185：增加独立SQLite待归档队列组件，要求专用连接；不复用SDK Session表、不保存执行token或backend对象，只收冻结MemoryRollout、授权revision及generate开关。原来源/分段身份键和完整payload哈希幂等，换授权版本不能覆盖原项；默认64MiB/256项、24小时过期（配置有界），相同重试不延长存活，确认删除要求原key+hash。读取复核来源模型/授权类型/存储长度/哈希/身份，正文不进入repr或错误。

三文件Python73通过、0失败/错误/跳过，`.tmp/goal-g4185-archive-queue.xml`已回读；包含SQLite serialize后关闭原连接并在新连接deserialize恢复、重复确认/容量/过期/损坏反例。测试仅内存SQLite，不是实际磁盘持久性或进程重启验收。组件尚未接生产捕获/配置/后台恢复；仍需后端重新授权合同，不能保存旧执行凭据来规避原身份门禁，也不能将新增组件视为归档已可靠。

当前剩余77%，未到70%停止线。跨进程归档生产闭环、旧工作区、Go行为/真实联合、同版完整验收和最终全局review未完成；未操作实际服务或用户数据库，Goal保持active。

### G4.184 实现与验证记录

G4.184：更新同版完整前端回归证据，测试期间未改应用源码。npm test全套72文件1051项、0失败/错误，`.tmp/goal-g4184-frontend-all.xml`已回读，测试进程退出0；tsc -b退出0。JUnit记录time=66.5193825是报告值，不作为整个命令墙钟耗时。组件/API替身测试，不包含浏览器、真实Go/网关/OCI，不替代端到端能力验收。

本轮重读原完成态十一条，范围仍含Skill零核心改码、实际安装调用、三模式、统一工具策略、Projection、真实流、SDK Session/原生压缩、隔离、文件交付和逐项证据。没有因前端全绿关闭后端缺口或最终review。跨进程归档、旧工作区、Go行为和真实联合及最终全局review保持未完成。

实时额度剩余77%，未到70%停止线；未启动/重启服务、未访问测试机或用户实际数据库，Goal保持active。

### G4.183 实现与验证记录

G4.183：归档明确拒绝与结果未知分离。授权查询、archive写入或未知结果回读收到401/403/409/410时，状态置rejected，释放归档状态持有的原SDK result、冻结分段、source、policy及archive snapshot引用，原context不再重复授权/归档。未知错误仍保留原字节与原回读逻辑。仅清理这组内存引用，不宣称删除其他运行状态/日志/磁盘或安全擦除。

三文件Python82通过、0失败/错误/跳过，8.01秒，`.tmp/goal-g4183-archive-revocation.xml`已回读。新增明确拒绝四状态和授权/回读拒绝反例；原结果未知、原事务确认及三执行来源覆盖保持。此修复是持久归档恢复之前的撤销语义纠错，跨进程outbox仍未实现。已检查Sidecar现有持久化仅SDK Session SQLite，不能把私有归档正文无隔离地混入Session或持久旧执行凭据。

实时额度剩余77%，未到70%停止线。跨进程归档、旧工作区、Go行为/真实联合、同版完整回归及最终全局review未完成，Goal保持active；未操作实际服务或数据库。

### G4.182 实现与验证记录

G4.182：补过期发布收尾四场景Go源码，通过原工具注册/本人审批/工具start、原生snapshot和PublishMemory API建立发布证据，分别保持工具在途、完成工具、用户取消、发布后编辑；断言调度不重新claim，只有确切完成且无后续编辑者收尾，原worker完成重试保持revision。输入计划仅使用隔离选择记录夹具，不将其称为完整SDK输入准备或真实OCI验收。

Go vet三个包退出0（包含测试源码类型检查），行为测试遵守既有限制未执行，本批没有新增运行通过数，不宣称正例已经通过。G4.181生产事务逻辑未进一步修改。当前该收尾仍欠Go行为证据及真实进程重启联合；不能以测试源码存在结项。

实时额度仍剩余78%，未到70%停止线。跨进程归档、旧工作区、同版完整回归及最终全局review未完成，Goal保持active；未操作实际服务/数据库。

### G4.181 实现与验证记录

G4.181：后端将完成事务的来源/权限、确切私有发布和未结工具校验提取为共享事务函数。独立调度claim在过期处理前，仅对expired running且started的整合任务核对同一完成合同；证明充分才记completed，不重跑SDK、不要求遗失的原token明文。暂停/取消/有效租约不进入该分支；不满足合同仍走原失联暂停，原直接complete接口保持身份和有效租约门禁，已完成精确重试仍用原token哈希。

Go build ./...及vet三个包退出0。新增五类负例源码（未发布、用户自行修改、取消、暂停、租约有效），行为未运行；尚缺确切发布成功和在途工具的正反例证据，不宣称调度收尾已验收。无SDK检查点收尾继续沿该事务合同补证据，不以源码存在结项。

实时额度仍剩余78%，未到70%停止线。跨进程归档、旧工作区、同版完整回归及最终全局review未完成；未操作实际服务/数据库，Goal保持active。

### G4.180 实现与验证记录

G4.180：补生产run_memory_consolidation成功恢复证据。实际流式SDK在工具完成后用户暂停，将checkpoint及原计划编码后解码为新attempt/token，再从生产入口恢复；断言不查询新基线、原输入计划提交一致、已完成工具只执行一次，工作区settle后校验完成回执，执行身份上下文清理。Provider/工作区/Go为替身，本批未改变生产逻辑，不称真实跨进程或真实发布验收。

五文件Python51通过、0失败/错误/跳过，7.319秒，`.tmp/goal-g4180-production-recovery.xml`已回读，测试进程退出0。下一代码缺口为已确认发布但缺少SDK检查点时的任务收尾；应沿现有后端“确切私有发布+无未结工具”的完成合同核对，不盲目重跑SDK。跨进程归档、旧工作区、同版完整回归与最终全局review仍未完成。

实时额度剩余78%，未到70%停止线；未操作实际服务或数据库，Goal保持active。

### G4.179 实现与验证记录

G4.179：生产整合恢复入口消费claim冻结输入计划。有checkpoint时复用其原SDK original_input和原计划文件/哈希，不再读取最新基线或重新选择；仍向后端用原计划确认持久回执，随后绑定原生工作区并通过既有stage_hash/策略/审批恢复验证。没有完整计划、工作区或有效状态则明确拒绝，旧记录不自动猜测迁移。RecoveredMemoryConsolidation只保存恢复stage，不伪造缺失的selection retained/removed历史。

七文件Python75项通过、0失败/错误/跳过，4.536秒，`.tmp/goal-g4179-input-recovery.xml`已回读。实际SDK审批检查点反序列化覆盖原额外输入保留、批准后工具一次、策略变化拒绝；生产入口反例断言缺计划不重新读取基线。模型/工作区/后端仍是替身，尚缺真实发布后重启、原生工作区与Go权限事务联合验收，不将这些测试称为跨进程完整恢复通过。

实时额度剩余78%，未到70%停止线。跨进程归档、无SDK检查点的完成恢复、旧工作区兼容、同版全量和最终全局review仍未完成；未操作实际服务/数据库，Goal保持active。

### G4.178 实现与验证记录

G4.178：私有Memory claim增加已持久input_plan及input_plan_hash（无计划时省略），仍只走独立worker claim；Sidecar严格校验阶段、完整字段对、来源/提取/基线、内容/计划哈希、大小和私有路径，正文不进入repr或错误。生产恢复尚未消费这些字段，下一步从已确认计划重建原SDK输入，不能把本批传输合同称为跨进程恢复完成。

同时修复实际跨语言合同缺陷：Memory输入计划由Go map排序编码，原Python按构造顺序计算计划哈希，模拟后端使用同一helper掩盖差异。现在仅在Memory计划payload规范排序顶层和files；共享native文件/PTY struct哈希保持字段顺序，不做全局排序。传输测试独立排序编码生成服务端哈希并断言键序，不再直接复用被测helper计算计划回执。

末版七文件Python116通过、0失败/错误/跳过，6.021秒，`.tmp/goal-g4178-input-contracts.xml`已回读；此前扩大含manifest结果132通过、5个tmp_path准备错误（WinError5既有临时目录拒绝访问），保留`.tmp/goal-g4178-frozen-input-claim.xml`，不宣称扩大回归全绿。Go build ./...及vet三个包通过，行为测试未运行。初始新claim正例失败揭示键序差异，修复后通过；未改系统策略或测试临时目录绕过限制。

实时额度剩余78%，未到70%停止线。跨进程归档/完成恢复、旧工作区兼容、同版完整验收及最终全局review仍未完成，Goal保持active；未操作实际服务或数据库。

### G4.177 实现与验证记录

G4.177：生产整合执行在complete请求超时后，使用同一claim/worker/attempt进行一次有时限的幂等确认重试，复用后端保留原token哈希的完成事务。不重新执行SDK模型或发布工具；第二次超时仍返回COMMIT_UNCONFIRMED；取消和BackendError（包括错误回执）原样传播、不重复重试。两个请求各自受request_timeout_seconds约束。

五文件Python62项通过、0失败/错误/跳过，耗时4.844秒，`.tmp/goal-g4177-completion-recovery.xml`已回读。覆盖流式与非流式首次超时恢复、持续超时、取消和回执拒绝，断言完成请求身份一致、模型只调用一次。后端/模型仍为替身，未运行Go行为测试或真实联合验收。本批仅修复进程存活期间的完成请求超时确认，不宣称解决跨进程丢结果、网络错误全类型或发布后重新规划问题。

当前账户已用22%、剩余78%，未到70%停止线。跨进程归档恢复、发布后完成恢复、旧工作区兼容、原范围同版完整回归和最终全局review仍未完成；未操作实际服务/数据库，Goal保持active。

### G4.176 实现与验证记录

G4.176：将整合阶段输入准备、审批暂停、提交失败和回执校验用例同时覆盖流式与非流式SDK路径，包含生产worker启用的cooperative_pause选项。四文件Python56项通过、0失败/错误，耗时4.628秒，`.tmp/goal-g4176-streamed-consolidation.xml`已回读。模型、工作区和后端仍为替身，不代表真实Go/OCI联合验收。本批仅扩展测试，未变更生产行为。

当前账户已用22%、剩余78%，继续执行，到剩余70%停止。跨进程归档恢复、发布后完成回执丢失恢复、旧工作区恢复兼容、原范围同版完整回归及最终全局review仍未完成。未操作用户服务或实际数据库，Goal保持active。

### G4.175 实现与验证记录

G4.175：修复用户暂停与审批暂停竞态。已记录PAUSE_REQUESTED时，晚到的审批checkpoint以USER_PAUSED落盘，原字节/哈希不变；丢响应重复查询保留该原因。审批暂停先落盘后直接用户再次pause，也转为USER_PAUSED并撤销原token，不保留可自动恢复的审批原因。Sidecar允许后端将审批暂停回执提升为用户暂停，但显式用户暂停仍不接受反向降为审批原因。

Python三文件51项通过、0失败/错误，`.tmp/goal-g4175-pause-race.xml`已回读；扩大实际SDK/HTTP回执反例。Go新增两种到达顺序、原回执重试及审批拒绝后不自动claim源码；build ./...及vet三包退出0，行为测试未运行。原范围真实联合、跨进程归档恢复、同版完整回归及最终全局review仍未完成。

末次账户额度剩余79%，未到70%停止线。未操作用户服务/实际数据库，Goal保持active。

### G4.174 实现与验证记录

G4.174：补协作暂停边界行为证据，未更改产品范围。新增真实HTTP客户端续租序列：原start、收到PAUSE_REQUESTED、后续续租保留请求；禁止回执清除请求/切换其他错误、未开始执行收到请求或无revision推进。新增实际流式SDK等待模型的取消/租约失效测试，断言模型finally完成、SDK任务结束、原身份上下文清理，私有错误不外泄。

四文件Python50项通过、0失败/错误，`.tmp/goal-g4174-pause-boundaries.xml`已回读。本批未发现需新增生产修复的反例；不重复把同一路径静态核对计为新功能。真实Go/原生工作区跨进程暂停恢复、归档跨进程恢复、原范围同版全量与最终全局review仍未完成。未操作实际服务/数据，Goal保持active；继续遵守剩余70%停止线。

### G4.173 实现与验证记录

G4.173：运行且已开始的Memory pause改为记录PAUSE_REQUESTED，保留status running/token/lease，尚未开始仍可立即暂停。续租回执严格允许从空error_code到该请求码且revision推进；worker生产入口启用流式SDK，续租事件触发after_turn，等待SDK任务结束及工作区settle，再持久USER_PAUSED检查点并撤销执行租约。正常完成先到达时仍按终态提交；取消/失联仍走原撤销和清理。

前端接受“请求暂停”的running回执，展示正在暂停并禁用重复暂停，保留取消，不提前显示已暂停；移除旧立即暂停警告。Python四文件49项通过，`.tmp/goal-g4173-cooperative-pause.xml`，含实际SDK工具在途、续租信号、工具完成后单次用户checkpoint持久回执；模型/后端为替身，不代表真实OCI/Go联合。前端两文件17项通过，`.tmp/goal-g4173-pause-ui.xml`已回读；tsc -b、Go build/vet通过。Go测试源码已更新请求/通知/完成/恢复状态顺序，未运行行为测试。

用户主动暂停的生产调用已接通，但三模式来源、真实原生工作区及跨进程恢复联合验收仍不足；不宣称安全暂停完整验收。跨进程归档恢复、原范围同版完整回归与最终全局review仍未完成，未操作用户服务/数据库，Goal保持active。

### G4.172 实现与验证记录

G4.172：持久pause复用同一接口，根据checkpoint.pause_kind区分USER_PAUSED与APPROVAL_PENDING，拒绝未知kind；旧缺kind按审批兼容。重复回执要求相同原因及哈希，保留原token的精确重试与暂停执行隔离。Sidecar persist_memory_pause增加显式user_requested，严格校验用户暂停回执，不能由审批暂停替代；审批自动恢复仍只选择APPROVAL_PENDING。

Python三文件33项通过，`.tmp/goal-g4172-user-pause-receipt.xml`，包含实际流式SDK用户暂停、真实HTTP测试传输及错误原因拒绝。Go build ./...和vet三包退出0；新增Go源码覆盖原因、重复回执、不会被普通claim自动恢复和显式继续，行为未执行。前端暂停请求/续租通知到SDK after_turn仍未接通，现有强制暂停行为未替换，完整安全暂停仍未验收。

本批末实时额度已用21%、剩余79%，尚未达到用户剩余70%停止线。跨进程归档恢复、原范围完整验收和全局review仍未完成，未操作实际服务/数据库，Goal保持active。

### G4.171 实现与验证记录

G4.171：Memory检查点增加pause_kind，旧记录默认approval；新增显式user_requested冻结分支，仅接受固定SDK已停止的stream、after_turn取消模式、无final_output及原阶段/输入/工作区。恢复用户轮次边界状态无需伪造审批；原审批分支继续要求interruptions。SDK context仍清除旧凭据。

三个Python文件46项通过、0失败/错误，`.tmp/goal-g4171-memory-user-checkpoint.xml`。新增提取/整合实际流式SDK测试，在工具完成时after_turn暂停、序列化重建后继续，工具仅执行一次。初始测试过早取消及误用非流式模型夹具导致失败，改为既有StreamingSequence和工具完成时暂停后通过，未修改SDK行为。后台暂停请求/续租信号及持久user-pause回执尚未接入，不宣称安全暂停完成。

用户新增剩余70%停止边界已记于顺序与边界，当前持续按实时额度检查。原范围补齐、完整验收与全局review未完成；未操作实际服务/数据库，Goal保持active。

### G4.170 实现与验证记录

G4.170：修复暂停Memory任务无检查点仍展示可用恢复按钮。后端列表返回resume_available，按paused且未开始或存在检查点计算，不读取/公开检查点正文；它仅表示初步恢复条件，原控制事务继续验证当前权限、完整性与来源。前端仅显式true可恢复，false显示缺少恢复检查点，旧后端缺字段显示条件未确认并禁用；取消入口保留。

新增Go测试源码覆盖queued/running/paused/completed、started及检查点组合，未执行行为测试。前端两文件16项通过、0失败/错误，`.tmp/goal-g4170-memory-resume-ui.xml`已回读，含false/缺字段均不发送恢复请求；tsc -b、Go build ./...和vet三包退出0。未做浏览器/真实后端联合验收或启动服务。

强制暂停仍可能没有可恢复检查点，本批只修正入口诚实性，不把禁用按钮当作安全暂停/恢复能力完成。归档跨进程恢复、原范围同版完整验收与全局review仍未完成，Goal保持active。

### G4.169 实现与验证记录

G4.169：新增原执行专用archive-receipt查询，无正文响应；同事务校验来源完整性/遗忘、确切授权revision及关联generation的用户/项目/workspace/来源哈希。允许原执行终态核验，不开放给Memory生成身份。旧来源没有回执或授权版本不匹配均拒绝；共同来源读取逻辑提为事务内helper。

Sidecar未知写入改用完整事务回执核验，不再以旧来源read作为成功依据；严格检查bool/int类型、用户/项目/activity/segment/hash/revision/生成开关/有界任务ID。原回执确认后清除本次冻结请求，不重发写入或模型；明确拒绝仍不重试。新增Go源码覆盖三模式回执、错误版本及遗忘拒绝，未执行Go行为测试。

三个Python文件77项通过、0失败/错误，`.tmp/goal-g4169-archive-receipt.xml`已回读；覆盖实际HTTP传输、丢响应后确切回执及字段/类型反例。Go build ./...、vet三包退出0，全部进程终态。跨进程待确认请求持久恢复仍未完成；同版全量/真实联合及全局review仍未完成。未操作实际数据库、服务或部署，Goal保持active。

### G4.168 实现与验证记录

G4.168：归档记录增加archive_revision/archive_generate/archive_generation_id，Schema源码升至76。授权归档及自动排队成功后，在同一事务写入确切授权版本、生成开关与任务ID；失败整体回滚。旧来源默认0/false/空ID，不迁移伪造授权回执。遗忘同事务清除新增字段，原配额每来源1024字节元数据预留覆盖这些有界字段。

新增/扩展Go测试源码覆盖三模式归档授权回执、关联任务确切ID、遗忘清除及旧save不获得回执。Go build ./...、vet三包退出0，所有命令终态；未执行Go行为测试或打开/迁移实际用户数据库。

完整回执读取接口和Sidecar消费尚未接入，来源只读确认仍不能报告自动排队成功。跨重启持久恢复、原范围同版完整验收与全局review仍未完成，Goal保持active；未部署或操作用户服务/数据。

### G4.167 实现与验证记录

G4.167：回查W0-W9原清单与旧路径，确认生产自动捕获已使用archive，旧POST rollouts仍可绕过授权。旧HTTP写入口现经内部认证后固定返回410/AGENT_MEMORY_ARCHIVE_CONSENT_REQUIRED，不解析正文、不调用Store写入；读取与新授权入口保留。Store旧方法仍用于测试夹具，Python旧客户端函数保留显式失败兼容，不再有成功的旧HTTP写通道。

Go build ./...、vet三包退出0，新增旧HTTP认证/410/no-store/不回显正文测试源码，未执行Go行为测试。Python传输与自动捕获两文件50项通过，报告`.tmp/goal-g4167-archive-consent.xml`，含410只发一次请求、不切换或回退其他写接口。此为W7授权/W9旧路径清理，不签署整组或最终review。

原清单中持久恢复、完整归档事务回执、同版跨层验收与全局review仍未完成；范围待明确的能力不自动排除。未操作用户服务/实际数据或部署，Goal保持active。

### G4.166 实现与验证记录

G4.166：修复 SDKMemoryWorker 停止关闭模型客户端后仍能 start/run_once 领取任务的问题。停止实例明确不可复用，须由新实例取得新客户端；停止开始立即禁止新领取，跟踪并取消后台循环或直接run_once在途任务，等待其结束后关闭客户端。并发stop共享单个shielded清理任务，客户端仅关闭一次；内部执行调用自身stop明确拒绝以免死锁。

worker及app生命周期两文件20项通过、0失败/错误，报告 `.tmp/goal-g4166-worker-lifecycle.xml` 已回读。新增停止后拒绝重启/领取、并发停止单次close、直接执行取消先于close及等待领取拒绝测试。未启用实际worker或重启服务。

归档持久恢复/完整事务回执、异常与历史任务恢复、原范围同版完整回归及全局review仍未完成；此前临时目录权限错误和Go行为验收缺口不因局部通过而消除。Goal保持active。

### G4.165 实现与验证记录

G4.165：自动归档准备不再依赖原生工作区预先解析snapshot。无预存来源时在Runner前使用原执行身份读取并校验Memory snapshot，再固定授权；读取失败不启动捕获，不把记忆失败转成业务重跑。核对主对话终态修复仍经过相同Runner包装，提交在commit工具内完成，并非另有Runner返回后的直接提交路径。

未知写入结果仅尝试一次有界只读核验，校验原segment、来源身份及完整字节哈希；明确授权冲突不触发读取或重写。旧read只证明来源存在，不能证明本次授权事务及自动排队成功，因此读取成功仅标 source_confirmed 并保留原冻结请求，不标整步成功。进程重启后的持久恢复和完整归档事务回执仍待实现。

六文件Python回归179项通过、0失败/错误，`.tmp/goal-g4165-memory-recovery.xml` 已回读；新增非原生三模式快照解析、写入丢响应只读确认和撤销后不重试测试。首轮新增夹具使用旧占位哈希导致3项失败，修正为真实内容哈希后通过，未放宽生产验证。此前G4.164的47项临时目录权限错误仍未解决，不能以本次子集替代完整回归。原范围同版验收、全局review仍未完成；未操作用户服务/实际数据，Goal保持active。

### G4.164 实现与验证记录

G4.164：原生工作区准备保存本次 Memory snapshot，native_run_config 在运行前读取并固定归档授权。三执行路径接入已结束SDK结果捕获；主对话还要求 commit_result，后台在输出规范化后调用，状态执行在 finish_generation 后调用。无输出/审批中结果不捕获；生成任务不递归归档。归档保留原身份与授权版本，后端 archive 增加原执行 terminal 访问例外，仍受来源/当前权限/授权版本校验。

归档异常不传播为业务重跑，日志仅异常类型；未知结果在本次 context 保留冻结字节并停用后续捕获，不自动重试。当前仅原生工作区路径会准备授权，未覆盖非原生工作区；不捕获失败/暂停段，进程重启后的持久恢复、晚于Runner返回的主对话提交路径仍需核对，不视作完整自动归档验收。

局部Python三文件63项通过，`.tmp/goal-g4164-memory-autocapture.xml`。扩大六文件回归122项通过、47项初始化错误，退出1，`.tmp/goal-g4164-execution-regression.xml`：错误来自 pytest临时目录 WinError5，不能将其计为通过。Go build ./... 与 vet 三包退出0；未运行Go行为测试或真实后端联合验收，未绕过系统策略。原范围完整回归和全局 review 仍未完成；未操作用户服务/实际数据库，Goal保持active。

### G4.163 实现与验证记录

G4.163：新 archive 通道在授权允许 generate_enabled 时，与归档同事务创建 extraction 队列任务。提取内部事务级排队函数，复用手动排队的来源完整性、当前成员权限、Memory基线、配额和来源唯一去重；旧 save 不自动排队。写入或排队失败整体回滚，重复归档及用户手动排队复用同一任务。授权关闭阻止新自动排队，不追溯取消既有任务；既有任务继续由显式控制/遗忘处理。

新增 Go 测试源码覆盖三模式归档-only/自动生成、重复归档仅一任务、任务身份/来源哈希/初始阶段、手动排队去重，以及注入队列插入失败时归档回滚。Go build ./... 与 vet 三包退出0；未运行 Go 行为测试，事务与并发行为尚未执行验收。

已定位三执行路径的暂停/续跑和SDK结果返回边界，但普通执行自动捕获调用尚未接通，不把 archive 接口内自动排队等同于用户完整自动流程。旧无授权归档接口收敛、异常/历史任务恢复、原范围同版完整回归和全局 review 仍待完成。未操作用户数据、实际数据库、服务或部署；Goal 保持 active。

### G4.162 实现与验证记录

G4.162：新增原执行专用 POST archive-policy/archive。写入事务内复核来源身份、archive_enabled 与精确 consent_revision；撤销或重新授权后旧版本请求均拒绝，生成任务不能归档自身。新增 Go 测试源码覆盖三执行模式默认拒绝且不落库、授权读取、成功及重复回执、撤销/重新授权后的旧请求拒绝和生成身份拒绝。

Sidecar 新增严格授权回执模型及读取/归档函数，校验用户、作品、显式 bool 与授权版本；归档发送冻结的 SDK 来源及授权版本，失败不回退旧 save 接口。原有传输边界与精确持久化回执检查保留。两文件 Python 回归57项通过、0失败/错误，报告 `.tmp/goal-g4162-memory-archive.xml` 已回读；Go build ./... 与 vet 三包退出0，Go 行为测试未执行。

普通对话/后台/状态执行结束时的自动捕获调用和自动排队尚未接通；旧 rollouts/save 无授权接口仍存在，需在调用迁移后收敛，不能将本批视作自动归档能力完成。异常与历史任务恢复、原范围同版完整回归和全局 review 仍未完成。未部署、迁移实际数据库或操作用户服务/数据，Goal 保持 active。

### G4.161 实现与验证记录

G4.161：新增用户 GET/PUT memory/preferences，PUT 要求显式两项 bool 和 revision、4KiB请求；复用直接用户身份、CAS与回执。现有项目 Memory 窗口增加按需展开授权区，区分归档和自动生成，自动生成依赖归档，viewer不能保存。校验本人回执与原 request_id/revision/flags；未知结果保留原请求重试，不默认启用。Memory版本改变时重新挂载读取授权，涵盖遗忘后的撤销显示。

末版 tsc -b、Go build ./... 与 vet 三包退出0。两个前端文件11项通过、0失败/错误，`.tmp/goal-g4161-memory-consent-ui.xml` 已回读，覆盖依赖约束、保存、丢响应原请求重试、他人回执拒绝及原管理回归。新增 HTTP 测试源码覆盖默认读取、缺失授权字段、viewer拒绝和同请求回执；未运行 Go 行为测试。

授权设置入口已具备，但自动归档触发/授权读取/自动排队仍待连接，不表示普通对话已自动归档。异常与历史任务恢复、原范围同版完整回归和全局 review 仍未完成。未迁移实际数据库、部署或操作用户服务，Goal 保持 active。

### G4.160 实现与验证记录

G4.160：新增用户/作品隔离的 Memory preferences 存储，archive_enabled 与 generate_enabled 默认 false，自动生成依赖归档授权，不复用 read_enabled。直接用户 editor 才可更新，revision CAS、最新 request_id/hash 精确重试；读取默认0版本。新表计入配额，项目删除级联；遗忘全部同事务撤销两项授权、增加revision并清除旧请求，旧授权请求重试不能重新开启。

Schema 75 仅更新迁移代码和注册，没有打开/迁移实际用户数据库。新增测试源码覆盖默认关闭、授权/重复回执/改参拒绝、委托身份拒绝、依赖约束与遗忘撤销/旧请求拒绝。Go build ./...、vet 三包退出0，未运行 Go 行为测试或真实迁移验收。

设置 HTTP/UI 和执行中的授权读取/自动归档/自动排队尚未接入，此存储不代表已启用后台收集。异常/历史任务恢复、原范围同版完整回归与全局 review 仍未完成。未部署或操作用户数据/服务，Goal 保持 active。

### G4.159 实现与验证记录

G4.159：生成任务区新增按需展开的归档来源选择器和“生成记忆”，仅编辑角色可见。只使用后端本人来源列表，校验 owner/project、引用格式、hash、重复项和游标；已有关联任务的来源不可重复选择。排队仅提交三项来源引用，确认任务回执的归属/来源/hash/id/revision/status 后刷新来源和任务。

未知排队结果保留原引用并锁住来源切换，重试原请求；明确4xx拒绝可重新选择，408/COMMAND_IN_PROGRESS仍保留未知状态。支持分页、刷新、空列表和卸载取消读取，没有上传任意正文入口。

末版 tsc -b 退出0，三文件17项前端测试通过、0失败/错误，`.tmp/goal-g4159-memory-sources-ui.xml`；覆盖实际选择、精确引用、已创建来源禁用、未知重试、他人来源拒绝及旧 Memory 管理/控制回归。未做浏览器视觉或真实后端联合验收。

自动归档触发仍未接通，仅已有 SDK 归档可供选择；自动授权设置、异常/历史任务恢复、原范围同版完整回归和全局 review 仍未完成。未部署或操作用户数据/服务，Goal 保持 active。

### G4.158 实现与验证记录

G4.158：新增本人作用域 GET projects/{id}/memory/sources，以 activity_key/segment_id 稳定排序、每页50项，返回来源哈希及已关联 generation_id，不读取 envelope 正文。仅直接用户可调用；筛掉 forgotten，游标采用有界编码的引用二元组并要求属于本人未遗忘来源；失效游标要求刷新。列表不替代排队时的完整来源校验。

新增测试源码覆盖本人来源/任务关联、尾页、其他成员空列表及游标拒绝、委托身份拒绝、遗忘隐藏和旧游标失效。Go build ./...、vet 三包退出0；未运行 Go 行为测试或多页联合验收。

前端来源选择尚待接入，自动归档/用户授权、异常/历史任务恢复、原范围同版完整回归和全局 review 仍未完成。未部署或操作用户数据/服务，Goal 保持 active。

### G4.157 实现与验证记录

G4.157：新增用户 POST projects/{id}/memory/generations，4KiB 严格请求仅接受 activity_key/segment_id/source_hash，复用 QueueAgentMemoryGeneration 的直接用户授权、editor 权限、本人已保存来源和原哈希校验；不接受前端上传正文冒充 SDK rollout。来源重复排队仍沿现有幂等语义。

扩展 HTTP 测试源码覆盖本人列表、空请求、viewer 排队、缺失来源和夹带 rollout_jsonl 拒绝。Go build ./...、vet 三包退出0；未运行 Go 行为测试，因此实际 HTTP 成功排队及否定场景未做联合执行验收。

来源清单/前端选择仍未接入，自动归档/授权、异常与历史任务恢复、原范围同版完整回归与全局 review 仍待完成。未部署或操作用户数据/服务，Goal 保持 active。

### G4.156 实现与验证记录

G4.156：领取事务增加审批就绪恢复检查，处理“用户决定先于 SDK pause 落盘”的竞态。仅 status paused/error APPROVAL_PENDING、非空 checkpoint、当前阶段有已决调用且无 pending_approval 的任务可恢复；重新核验任务完整性/原来源/当前权限后入队并撤销旧 token。用户主动暂停/租约丢失不自动恢复，来源失效或私有状态无效保持暂停并标记 RESUME_REQUIRES_REVIEW。

一次最多检查50项并在原 claim 事务内执行。新增 Go 测试源码覆盖 pause-first 与 approval-first、两个审批未全决时不恢复、最后决定后新 attempt/token。Go build ./...、vet 三包退出0；未运行 Go 行为测试，实际审批到恢复联合验收仍缺失。

自动归档与用户授权、无检查点/历史任务/丢响应恢复、原范围同版完整回归和全局 review 仍待完成。未部署或操作用户数据/服务，Goal 保持 active。

### G4.155 实现与验证记录

G4.155：生成任务列表接入 pause/resume/cancel 控制，编辑角色才显示按钮，提交确切 expected_revision，校验回执身份/阶段/attempt/model/status 与 revision+1 后重新读取列表。请求错误或错回执锁住后续控制，必须成功刷新后才能再次操作，不自动重发未知请求。取消要求确认；运行中强制暂停提示无检查点可能无法恢复这一现有限制。

末版 tsc -b 退出0；两个前端测试文件14项通过、0失败/错误，`.tmp/goal-g4155-memory-controls.xml`。新增精确修订号/确认后刷新、未知结果锁定与只读角色无控制按钮测试。没有浏览器视觉或真实后端联合验收，未启动/重启服务。

当前控制是已有后端直接暂停/恢复语义，并非 graceful checkpoint pause；此恢复缺口仍待处理。自动归档与授权、审批自动继续、异常/历史任务恢复、原范围同版完整回归和全局 review 仍未完成。未部署或操作用户数据，Goal 保持 active。

### G4.154 实现与验证记录

G4.154：现有项目记忆 dialog 增加按需展开的生成任务列表，使用本人分页 API，显示阶段/状态/模型/尝试次数/时间及错误码，提供刷新和加载更多。加载、空列表、错误与卸载取消有明确处理；校验返回归属、字段类型、重复 ID 和分页游标，拒绝展示他人任务。原手动记忆编辑/保存流程保留，尚未添加任务控制按钮。

末版 tsc -b 退出0；两个前端测试文件11项通过、0失败/错误，`.tmp/goal-g4154-memory-ui.xml` 已回读。新增分页/他人响应拒绝/错误刷新与 abort 验证，原 ProjectMemory 八项回归通过。未启动/重启服务或进行浏览器截图/视觉验收，未操作用户数据。

仍需用户任务控制、自动归档与授权、审批自动继续、异常/历史任务恢复、原范围同版完整回归与全局 review。Goal 保持 active，未部署。

### G4.153 实现与验证记录

G4.153：核对用户入口发现前端仅有手动 Memory 管理，后端只有按 ID get/control，没有列表。新增本人作用域的 GET projects/{id}/memory/generations，按 created_at/generation_id 倒序游标分页，每页50条；游标必须属于当前用户/项目/工作区，拒绝服务或委托身份。列表只 SELECT 状态摘要，不读取 checkpoint/extraction/input plan/token 正文，响应 no-store。

新增测试源码覆盖列表身份、本人游标、他人空列表与他人游标拒绝，以及故意损坏私有检查点仍可读取状态摘要且不泄露内容。Go build ./...、vet 三包退出0，未运行 Go 行为测试；分页多页行为和 HTTP 联合行为尚未验收。

前端列表/控制入口尚未接入，自动归档与授权、审批自动继续、异常/历史任务恢复、原范围同版完整回归与全局 review 仍待完成。未部署或操作用户数据/服务，Goal 保持 active。

### G4.152 实现与验证记录

G4.152：新增默认 false 的 CONTENT_AGENT_SIDECAR_MEMORY_WORKER_ENABLED 配置，app lifespan 可独立创建一个 Memory Worker，不依赖业务 task_worker_enabled；沿现有 shielded shutdown gather 等待退出，启动失败同样清理。没有改环境变量或重启服务，所以当前环境未开启。

SDKMemoryWorker.from_settings 复用既有控制模型 endpoint/key/name/timeout/retries 和输出限额，使用 OpenAIResponsesModel，不新增模型/网关配置。复用任务轮询/租约参数并在创建客户端前验证边界；策略 hash 使用稳定排序的策略/模型配置；stop 关闭其自有模型客户端。

末版三文件15项通过、0失败/错误/跳过，2.82秒，`.tmp/goal-g4152-memory-lifespan.xml` 已回读，覆盖默认禁用、独立启动、启动失败清理、旧 Worker 生命周期、工厂模型映射/guardrails/客户端关闭。生命周期和模型客户端为替身，不是真实任务领取或模型请求验收。

仍需自动归档触发、用户授权与操作入口、审批自动继续、异常/历史任务恢复、原范围同版完整回归和全局 review。未部署、运行 Go 行为测试或操作用户数据/服务，Goal 保持 active。

### G4.151 实现与验证记录

G4.151：新增 SDKMemoryWorker 可调用调度组件，独立领取任务、同一实例串行处理、按 extraction/consolidation 调用已有执行器，不复制 Agent loop。明确接收模型/策略/worker 身份与边界配置，为原 conversation/attempt 构建上下文，关闭 tracing；模板拒绝业务工具/MCP/handoff，并应用平台 guardrail。字符串模型必须与领取 model_id 相同。

提供幂等 start 和取消并等待 stop；轮询异常仅记录异常类型，不打印私有内容。未确认尝试不由 Worker 自动重放，后端租约/暂停策略仍控制再次领取。尚未接入 app lifespan、配置工厂或授权用户入口，不会自动处理现有数据。

末版三文件27项通过、0失败/错误/跳过，`.tmp/goal-g4151-memory-worker.xml` 已回读，覆盖空队列、阶段分派、原身份/禁 tracing 与取消等待。Worker 分派测试替换执行器，既有整合用例仍使用 SDK Runner 替身环境；不是生产轮询联合验收。

仍需服务生命周期/模型配置接入、自动归档与授权入口、异常/历史任务恢复、原范围同版完整回归及全局 review。未部署、运行 Go 行为测试或操作用户数据/服务，Goal 保持 active。

### G4.150 实现与验证记录

G4.150：run_memory_consolidation 正常 SDK 结束后不再只返回原结果。先 settle 原生工作区、确认并停止租约续期，再退出委托身份，通过独立 complete_memory_generation 等待严格类型化后端完成回执；MemoryConsolidationRun 增加 completion。审批中断继续原 atomic pause，不走完成。收尾超时报告 MEMORY_GENERATION_COMMIT_UNCONFIRMED，后端无发布回执或错回执不能当作完成。

Python 四文件65项通过、0失败/错误/跳过，5.52秒，`.tmp/goal-g4150-memory-completion-execution.xml` 已回读。新增执行顺序、完成失败/超时/错身份与暂停分支验证，实际 SDK Runner、实际 BackendClient 完成校验，后端/原生 owner 为替身；不是 Go/OCI 联合验收。未运行 Go 行为测试。

保存后丢响应恢复、历史任务/清单恢复、生产轮询调度、自动归档/授权用户入口和原范围同版回归/全局 review 仍待完成。未部署或操作用户数据/服务，Goal 保持原完整范围 active。

### G4.149 实现与验证记录

G4.149：输入规划调用原 SDK write_phase_two_selection 生成记录，恢复原已保存 selection 不改基线，将新记录放入 .agent-memory-input/phase_two_selection.json 私有候选路径。仅 updated_at 固定为原 rollout 的已验证时间以保证恢复重建稳定；SDK selected 结构/选择结果未重写。Runtime 清单允许该有界候选记录，阶段指令要求复制到 Memory 并纳入同一完整发布快照。

发布意图登记和实际 PublishMemory 均要求生成任务的 selection 文件与持久化 input plan 候选精确相同，不能漏掉或换内容；普通 Memory 手动发布不变。历史无候选清单仍可读取，但生成发布明确拒绝，尚需显式恢复方案。初次 Python 测试发现 Windows Path 反斜杠取键错误，已改 as_posix。

末版四文件26项通过、0失败/错误/跳过，2.81秒，`.tmp/goal-g4149-memory-selection.xml` 已回读；验证 SDK 记录结构/原选项/稳定重建及既有整合路径。Go 增加缺失/变更/精确候选校验测试源码；build ./...、vet 三包退出0，未运行 Go 行为测试。尚非实际发布联合验收。

后续仍须执行器自动收尾、保存后恢复、旧清单恢复、生产调度/归档/授权入口、原范围同版回归与全局 review。未部署或操作用户数据/服务；Goal 保持 active。

### G4.148 实现与验证记录

G4.148：Sidecar 新增 complete_memory_generation 独立调度传输，要求 running consolidation claim，沿原私有 transport 的无委托身份、响应大小、禁止重定向等边界调用 complete。新增独立 MemoryGenerationCompletion 类型，不放宽可执行 MemoryGenerationJob 的状态范围；完成回执逐项比较原身份/来源/基线，要求 started、空 lease/error、空 checkpoint hash 和前进的 revision。

Python 三文件71项通过、0失败/错误/跳过，5.49秒，`.tmp/goal-g4148-memory-completion.xml` 已回读。新增真实回环 HTTP 测试覆盖同一原 claim 重试、错误阶段/委托身份未发请求、错身份/hash/attempt/status/清理状态/revision 回执拒绝，以及 completed 对象不能解码为可执行任务。HTTP 对端为测试替身，并非 Go 联合验收。

生产执行器尚未自动调用收尾；SDK selection 持久化、真实发布联合验证、保存后恢复、生产调度/自动归档/授权入口、原范围同版回归及全局 review 仍未完成。没有部署、运行 Go 行为测试或操作用户数据/服务，Goal 保持 active。

### G4.147 实现与验证记录

G4.147：新增独立 scheduler generations/complete 与 CompleteAgentMemoryGeneration，不接受模型正文作为完成证据。首次调用要求当前 started/running consolidation 租约及精确 worker/attempt/token，复查原来源/权限和本任务 base+1 持久化发布回执，拒绝仍未终结的整合工具调用。确认后清除租约/checkpoint、增加 revision；仅保留 hashed worker token 支持相同身份的丢回执重试，终态继续禁止委托执行。

新增测试源码覆盖委托身份、提取阶段、错误 token 和无发布回执拒绝且状态/revision 不变。Go build ./... 与 vet 三包退出0；没有执行 Go 行为测试，正向提交/重复提交/并发和真实发布联合行为仍待验证。

生产 Worker 尚未调用此接口，SDK phase_two_selection 持久化仍未接通，不能将后端收尾方法视为 SDK 整合全流程完成。后续仍需保存后恢复、生产调度、自动归档、授权/用户入口、原范围同版回归与全局 review。未部署或操作用户数据/服务；Goal 保持 active。

### G4.146 实现与验证记录

G4.146：prepare_memory_stage 仅为 consolidation 显式传入两个既有私有发布 FunctionTool，经 AgentToolProvider 包装登记/审批后装配到 SandboxAgent。原生绑定仍拒绝继承业务 tools/handoffs/MCP，仅接受准备器提供的两项私有发布工具且拒绝重复名称；提取阶段没有增加工具。发布指令要求完整 bundle、原准备参数、owner approval 和确切持久化回执，不以快照代表保存。

Provider 的生成身份登记允许整合来源上下文中的私有发布工具，最终阶段授权仍由 Go job 校验；HTTP 私有白名单仅新增 POST native-workspaces/{session}/memory-publications，共享 publications 仍禁止。实际 PublishMemory 继续检查当前租约、政策、同一 approved SDK call、快照及版本。

扩展回归82通过/6环境错误，6项旧发布测试在 pytest 临时目录 WinError5，未执行主体，未绕过权限。最终针对性五文件68通过、0失败/错误/跳过，4.87秒，`.tmp/goal-g4146-memory-publication-focused.xml` 已回读；新增真实 SandboxAgent 绑定及整合准备器工具选择验证，后端/环境仍为替身。Go build ./...、vet 三包退出0，未运行 Go 行为测试。

尚需联合验证真实私有发布、完成回执/保存后恢复和 SDK selection 持久化，再接生产调度/自动归档/授权用户入口，完成原范围同版回归与全局 review。没有部署或操作用户数据/服务；已有环境阻塞未解除，Goal 保持 active。

### G4.145 实现与验证记录

G4.145：SDK 审批恢复接收执行器提供的已验证 job.phase；仅 consolidation 允许 publish_agent_memory 中断，extraction 保持三个 native 工具，未知阶段拒绝。准备发布为无审批工具，仍不能作为审批中断恢复。复用既有原 SDK call id/arguments 登记和回执校验，running/completed 状态仍拒绝重放。

新增真实 SDK RunState 用例覆盖发布批准、拒绝、pending，以及提取阶段和已执行回执拒绝；替换实际后端，不执行真实 Memory 保存。Python 三文件49项通过、0失败/错误/跳过，4.22秒，`.tmp/goal-g4145-memory-publication-approval.xml`。没有运行 Go 行为测试或部署。

模型工具装配、发布 HTTP 路由、最终完成/保存后恢复、生产调度、授权与用户入口仍需接通；之后仍需原范围同版回归和全局 review。Goal 保持原完整范围，未操作用户数据/服务。

### G4.144 实现与验证记录

G4.144：私有工具目录按阶段提供能力，extraction 保持原三个 native 工具，consolidation 增加 prepare_agent_memory_publication/read/never 与 publish_agent_memory/write/always。目录和 Begin 共用阶段策略校验，拒绝错误审批、access、名称和外部 server；发布工具不能在提取阶段登记。

Sidecar 私有目录协议同步接收上述两项并严格校验策略，原生工作区允许可信私有目录包含发布描述符，但没有自动继承业务工具。新增 Python 目录策略测试与 Go 策略测试源码。Python 三文件26项通过、0失败/错误/跳过，8.43秒，`.tmp/goal-g4144-memory-publication-catalog.xml` 回读；Go build ./...、vet 三包退出0，未运行 Go 行为测试。

模型端发布工具装配、审批恢复、HTTP 发布路由和最终任务完成仍未接通，不表示已能从对话保存 Memory。生产调度/恢复、授权与用户入口、原范围同版自测和全局 review 仍未完成。未部署或操作用户数据/服务，Goal 保持原范围 active。

### G4.143 实现与验证记录

G4.143：生成来源校验增加自身持久化发布回执的窄例外，仅 consolidation、启用且未遗忘的 base+1 当前版本可进入查询。联合 generation tool binding、private publication、approved/consumed 的运行或完成工具调用，以及版本 request_id/request_hash/content_hash 验证同一任务、所有者和工作区。用户编辑或另一任务的版本不能替代本任务基线。

自身回执确认后，原 rollout 内容仍执行原完整性和遗忘检查，原 Memory 引用改核对指定历史版本的 hash/forgotten/read-enabled 状态，不因自身发布改变 current 而误撤销；没有替换原输入或忽略当前权限。新增测试源码检查用户更新到 base+1 仍拒绝，未运行 Go 行为测试。build ./...、vet 三包及修改文件 diff check 均退出0。

该查询的正向发布联合行为尚未验收。发布工具/审批恢复仍未开放给生成任务，任务最终完成、发布后恢复、生产调度和用户入口仍待接通，之后还需原范围同版回归和全局 review。没有部署或操作用户数据/服务，Goal 保持 active。

### G4.142 实现与验证记录

G4.142：最终发布接入前修复身份绑定缺口。memoryPublicationBindingTx 现在同时比较 MemoryGenerationID 和 MemoryGenerationAttempt，防止普通执行携带另一生成身份进入发布绑定。生成发布意图通过现有 nativeWorkspaceOwnerTx 获取已验证的阶段工作区 key，不再用不含阶段的 Memory snapshot activity key 查找工作区；普通三模式保持原路径。

新增 Go 测试源码覆盖普通三模式分别注入生成 ID、尝试次数、两者组合时拒绝。build ./... 与 vet ./internal/runtime ./internal/httpapi ./internal/agenttool 均退出0；未运行 Go 行为测试，不将静态检查视为发布联合验收。本轮没有开放生成任务发布工具或路由。

后续仍须接通私有发布工具及审批恢复、确认自身发布后的精确版本回执与任务完成、生产领取循环/自动继续、自动归档与授权/用户入口，再做同版完整回归及全局 review。未部署或操作用户数据/服务，Goal 保持原完整范围。

### G4.141 实现与验证记录

G4.141：新增 run_memory_consolidation，复用既有启动/租约监督/原生运行入口，在 start 后以当前生成身份读取私有基线，核对版本/current_version/hash/owner，再由 SDK storage 规划输入，并退出委托身份后通过独立 scheduler 固定清单。只有输入回执确认后才装配原生工作区和 SDK consolidation stage；输入持久化超时明确报告 MEMORY_GENERATION_INPUTS_UNCONFIRMED，不调用模型。

审批中断沿原 private SDK checkpoint/atomic pause 路径持久化，并返回 MemoryConsolidationRun 的已确认 pause；正常结果返回原 SDK result、stage、input plan 和 receipt，仍是待最终发布的运行结果，不宣称 Memory 已保存。恢复时重新从同一原始来源与当前相同基线生成确定的计划，持久化接口和 SDK stage/checkpoint hash 负责拒绝差异；未将原生合并选择改为自研算法。

末版 Python 七文件66项通过，0失败/错误/跳过，5.79秒，`.tmp/goal-g4141-memory-consolidation.xml` 已回读且进程退出0。新增用例实际执行 SDK storage/Runner，替换后端和工作区 owner 验证 baseline→inputs→bind→workspace→model→settle→pause 顺序、start/基线变更/输入失败/超时不执行模型，以及成功结果不自动发布；不是 Go/OCI 联合验收。diff check通过。

最终 Memory 审批发布、生产领取循环/自动继续、自动归档与授权/用户入口、同版完整回归及全局 review 仍未完成。未改 Go/前端、未运行 Go 行为测试、未部署或操作实际数据库/用户服务。Goal 保持完整范围，已有环境阻塞未关闭。

### G4.140 实现与验证记录

G4.140：NativeGenerationSource 增加可选 plan_hash。指定已持久化清单时，Runtime 每次初始化/资源读取均校验当前生成身份、来源和完整清单 hash，返回清单中的精确路径与内容；原始两文件来源路径保留。manifest 合并 Memory 基线和派生输入，不重复同一路径；仅 raw_memories.md 可以更新已有聚合，其他已存在文件内容不同则拒绝初始化，不以空初始化文件覆盖已有 Memory。

Sidecar 支持计划引用的 resource path，准备器核对 input_plan 与 input_receipt 的 generation/plan hash/content hash 后装配来源引用和逐文件 SHA256；私有 manifest 恢复要求派生文件集合与哈希完整相等，不接受缺文件、换内容或换清单。执行入口可接收成对的 input_plan/input_receipt 且必须启用可信 native policy；这不替代尚未接通的 worker 生产调度和模型结束后的整合发布。

Go 新增测试源码覆盖真实 Runtime manifest 准备、按路径回读计划文件、错 plan hash 拒绝和 forget 后资源不可读。末版 build ./...、vet ./internal/runtime ./internal/httpapi ./internal/agenttool 均退出0；未运行 Go 行为测试。Python六文件42项通过，0失败/错误/跳过，4.76秒，`.tmp/goal-g4140-memory-initialization.xml` 已回读且进程退出0。测试含 SDK manifest 构建及缺失/内容/引用错绑拒绝，尚非 OCI 引擎联合验收。diff check通过。

生产 Worker 连续执行、整合审批发布、自动归档/授权排队与用户入口仍未闭环，同版完整回归与全局 review 未完成。未部署或迁移实际数据库，未操作用户服务/数据。Goal 保持原完整范围，已有环境阻塞未关闭。

### G4.139 实现与验证记录

G4.139：新增 PrepareAgentMemoryInputs 与内部 generations/inputs 路由。仅独立 service 调度身份、当前有效且 started 的 consolidation 尝试可以提交；复核来源、当前 Memory 基线、提取 hash，限定原 rollout、当前 raw memory/摘要、aggregate 以及必要空初始化文件的固定路径，验证内容集合 hash/体积。第一次提交必须在整合工作区或 SDK checkpoint 出现前完成；已有完全相同清单可读回同回执，差异不能覆盖。

Schema 74 增加 input_plan_json/input_plan_hash，作业加载检查完整性；计入存储配额，用户 forget 同事务清除，项目删除沿 generation 行删除。只修改迁移代码，没有打开或迁移实际用户数据库。该记录仍是可信 SDK worker 的派生输入，不接受模型委托调用；文件内容尚未接入 native manifest 写入授权。

Sidecar persist_memory_input_plan 校验 phase、基线、当前文件 hash，使用独立 scheduler inputs 传输，要求 generation/plan hash/content hash 完全匹配回执。Go build ./...、vet ./internal/runtime ./internal/httpapi ./internal/agenttool 均退出0；Go 测试源码覆盖未 start、委托身份、冻结/同回执/差异拒绝和 forget 清除，未运行 Go 行为测试。Python四文件39项通过，0失败/错误/跳过，5.34秒，`.tmp/goal-g4139-memory-input-receipt.xml` 已回读且进程退出0，包含真实回环HTTP客户端回执/身份隔离，不是 Go 联合验收。diff check通过。

仍需把清单记录接入 manifest 初始化，并接通生产 Worker/自动继续、整合审批发布、自动归档/授权及用户入口，再做同版完整回归和全局 review。既有环境阻塞未关闭。未部署或操作用户数据/服务，Goal 保持原完整范围。

### G4.138 实现与验证记录

G4.138：在派生输入授权的接入检查中发现阶段共享工作区的问题：原 memory generation 的 native owner key 只有 generation ID，提取阶段已冻结的 manifest 会被整合阶段复用，导致整合阶段不能初始化自己的输入。现由 Runtime 根据已验证 job.Phase 为 Memory native owner key 增加阶段后缀，epoch 也绑定该阶段和本次 token；普通三执行模式的 key 不变。同阶段仍按原工作区租约/快照协议恢复，不跨阶段复用初始化清单。

若发现历史无阶段后缀的 Memory workspace，明确返回 NATIVE_WORKSPACE_EXECUTION_STALE，不迁移、不删除、不把原检查点静默当作新工作区。该历史情况仍需显式恢复处理，不能据此宣称旧状态已兼容迁移。

Go 测试源码增加提取→整合不同 session、同阶段 current 保留与旧无阶段 key 拒绝用例。末版 build ./... 和 vet ./internal/runtime ./internal/httpapi ./internal/agenttool 均退出0，diff check通过；未运行 Go 行为测试。本轮未修改 Sidecar/前端、未部署、未操作实际数据库/用户服务。

派生输入文件的持久化授权尚未接通；生产调度/自动继续、整合审批发布、自动归档与用户入口、同版完整回归及全局 review 仍未完成。保留原完整 Goal，已知环境验收限制仍未解除。

### G4.137 实现与验证记录

G4.137：新增 plan_memory_consolidation。SDK SandboxMemoryStorage 的准备写入不能直接绕过 Runtime 的批准文件操作，因此增加只在内存暂存 SDK storage 数据 IO 的规划适配器：仅接收验证过 owner/content hash 的基线文件，限制两个私有目录、256文件和16MiB，不访问宿主文件系统或实际工作区，也不能执行命令；仅支持 SDK ensure_text_file 的精确 test -f 存在性查询。

原 SDK formatter、selection、rebuild_raw_memories 和 consolidation prompt 仍由既有 prepare_memory_consolidation 调用；没有重写选择/排序或模型编排。输出是相对基线变化的 UTF-8 文件集、内容 hash、基线版本/hash 和原 SDK stage/selection。保持基线不变、不提前写 phase_two_selection.json。该对象不是文件写入授权，下一步仍须将确定的派生输入接到持久化准入/初始化，不能直接用它发起免审批文件 RPC。

末版 Python 三文件23项通过，0失败/错误/跳过，6.63秒，`.tmp/goal-g4137-memory-input-plan.xml` 已回读、进程退出0。覆盖实际 SDK storage 生成、稳定结果/hash、基线不变、错误 owner/hash/disabled/path 拒绝与规划适配器的命令/路径/体积边界；未使用实际工作区/引擎。未修改 Go/前端、未运行 Go 行为测试、未部署或操作用户数据/服务。

尚未接通派生输入文件的写入授权、生产 Worker、整合审批发布、自动归档/授权排队及用户入口，完整同版回归和全局 review 未完成；Goal 保持原完整范围，已有环境阻塞未关闭。

### G4.136 实现与验证记录

G4.136：Native manifest 新增 generation 来源引用（generation_id/source_hash/extraction_hash），只允许当前有效的 consolidation 生成身份读取已持久化的 rollout.jsonl 和 extraction.json，目的路径固定为 `.agent-memory-input/` 两个文件。文件内容由 Runtime 从已验证来源/提取回执读取，不接受 caller 自带内容；初始化与后续资源读取沿既有 manifest hash、大小、一次性 materialization/seal 和 sandbox policy 校验。每次读取重新验证当前生成身份、用户访问、Memory 基线和未被遗忘的来源。

Sidecar 原生准备从已验证 claim 提取 generation 引用，传入 manifest sources；恢复校验要求两个输入完整、引用匹配且路径没有别名。资源读取/SDK File.apply 仍沿原协议。普通项目发布在 Go/Python 两侧均禁止 `.agent-memory-input` 源路径和目的路径，包含大小写别名；没有把私有初始化变成任意免审批写入。

末版 Go build ./... 与 vet ./internal/runtime ./internal/httpapi ./internal/agenttool 均退出0。新增 Go 测试源码覆盖精确整合身份、原始内容、普通/旧尝试身份拒绝及错提取 hash，未运行 Go 行为测试。Python指定文件和节点77项通过，0失败/错误/跳过，12.95秒，`.tmp/goal-g4136-memory-manifest.xml` 已回读、进程退出0；包含原资源 HTTP/发布路径回归，未运行依赖受限 tmp_path 的完整引擎测试。diff check通过。

这里只接通了原始 rollout/已确认提取产物的初始化准入；SDK raw_memories/summary/selection 的实际准备仍需接入，不把原始文件到位当作 consolidation 已完成。生产 Worker 调度/自动继续、整合审批发布、授权/自动归档排队/用户入口、同版完整回归和全局 review 仍未完成。未改用户数据/数据库或部署，已有环境阻塞未关闭，Goal 保持原完整范围。

### G4.135 实现与验证记录

G4.135：新增 prepare_memory_stage 并接入 Memory 执行入口的 native_policy 路径。仅在 start 回执已确认、生成租约监督已进入且当前生成身份已设置后，创建无业务 runtime tools/Skill 的 AgentToolProvider，加载私有目录，通过既有 NativeWorkspaceExecution 绑定原生 Agent，再验证准备后的 stage/checkpoint 并执行 SDK。prepared tools 生命周期包含模型和原生收尾，失败沿原路径退出，不另写工作区协议。

恢复时从已验证 claim 装配原生工作区引用，原 NativeWorkspaceExecution 负责与后端当前会话/环境/快照比对。错误策略类型、重复 owner、已有不同工作区引用，以及试图给原本无工作区的检查点新增原生环境，均在 start 前拒绝。未请求原生准备的路径保留现有行为；策略来自可信调用方，而非模型参数或检查点。

末版 Python 八文件96项通过，0失败/错误/跳过，15.28秒，`.tmp/goal-g4135-memory-preparation.xml` 已回读，进程退出0。新增测试实际调用 SDK Runner，替换目录/工作区准备器以验证 start→目录→工具作用域→绑定→workspace运行→模型→settle→退出顺序，以及 start/prepare/bind 失败不执行模型与准备作用域退出。已有私有 manifest/检查点/审批/提取回归通过；不是实际 OCI 引擎或生产 Worker 联合验收。

仍待生产 Worker 领取循环及自动继续、整合来源文件的私有准入、整合产物审批发布、授权/自动归档排队及用户入口。完整同版回归和全局 review 未完成，既有环境阻塞不变。本轮未修改 Go/前端、未运行 Go 行为测试、未部署或操作用户服务/数据。Goal 保持原完整范围。

### G4.134 实现与验证记录

G4.134：Memory stage 执行接入既有 native_run_config / settle_native_stream 生命周期。在生成身份与生成租约监督内运行 SDK，并在离开原生工作区运行范围前保存暂停快照或关闭已结束工作区，工作区收尾失败不提交提取完成。没有原生 owner 的纯文本提取保持原路径。

MemoryStageCheckpoint 新增可选 native_workspace 引用，复用 NativeWorkspaceCheckpoint 的 session/environment/state/snapshot 校验；SDK context 仍清空，不把旧凭据带入检查点。原生 owner 必须本轮已 settled，不能拿旧引用冻结尚未确认的工作区。恢复阶段要求调用方已准备的引用与原检查点一致；存在原生引用但没有准备 owner 时，在 start 前拒绝恢复，不静默降级成新工作区。原始检查点整体 hash 仍由领取回执验证，旧的无原生引用检查点可继续加载。

末版 Python 七文件89项通过，0失败/错误/跳过，21.21秒，`.tmp/goal-g4134-memory-native.xml` 已回读，进程退出0。新增真实 SDK checkpoint 的原生引用保留/错绑拒绝/不保存凭据测试，以及工作区 settle→退出运行范围→阶段提交的顺序及收尾失败拒绝提交测试；原生 owner 使用测试替身，包含已有私有 manifest 准备回归，但不是实际 OCI 引擎联合验收。未执行依赖已知受限临时目录的完整 native_runner/native_live_runner 测试，未解除环境阻塞。

仍待生产 Worker 领取与自动继续、生成来源文件的私有工作区准入、启动前原生绑定/恢复装配、整合发布与授权/自动归档排队/用户入口；完整同版回归与全局 review 未完成。本轮未改 Go/前端、未运行 Go 行为测试、未部署或操作用户服务/数据，Goal 保持原完整范围。

### G4.133 实现与验证记录

G4.133：新增 execute_memory_extraction，将既有 SDK stage 执行与审批暂停/提取提交接成单次阶段入口。沿用同一个 start、续租监督和审批恢复实现；仅接受 extraction phase。SDK 正常返回后验证原生提取输出，终止续租任务并检查剩余确认租约，在有界提交时限内使用独立 scheduler 身份调用 pause 或 extraction。终态事务会清除租约，因此先停止续租，避免提交成功后预期的 renew 拒绝反过来取消成功结果。

只有经过原有精确回执校验才返回 MemoryExtractionCompletion；审批中断返回已确认 paused job，提取结束返回已确认 extraction receipt。超时报告 MEMORY_GENERATION_COMMIT_UNCONFIRMED，不自动重试模型或将未知提交结果当作完成；外部取消继续传播。既有 run_memory_stage 保留返回实际 SDK result 的行为，不替代尚未完成的 consolidation 持久化。

末版 Python 五文件66项通过，0失败/错误/跳过，20.19秒，`.tmp/goal-g4133-memory-execution.xml` 已回读。覆盖真实 SDK 正常/空产物/审批中断、错回执/超时/取消、提交期间不再续租及跨阶段调用拒绝；新增真实回环HTTP BackendClient start→SDK→extraction 顺序测试。Go Runtime 为测试服务器替身，不是 Go 联合验收。测试服务器响应次数及非超时用例的过短测试时限已修正后重跑。本轮未改 Go/前端、未运行 Go 行为测试，未操作用户数据/服务或部署。

仍待生产 Worker 领取循环与自动继续、生成来源文件准备、原生工作区恢复引用、整合发布和授权/自动归档排队/用户入口；完整同版回归与全局 review 未完成，已有环境回归阻塞未关闭。Goal 不缩小范围。

### G4.132 实现与验证记录

G4.132：run_memory_stage 恢复真实 SDK RunState 后，在本次已启动且续租监督中的生成身份下读取私有工具目录，并用原 SDK call ID、原参数和当前生成尝试向既有 BeginAgentToolCall 接口重新取得持久化回执。Runtime 既有幂等分支负责验证原工具参数、生成任务及阶段；恢复器验证回执项目、会话、SDK ID、工具、审批状态一致性后才应用 SDK approve/reject，pending 保持中断，running/completed/failed 等状态拒绝自动重放。所有回执验证完成前不部分修改 SDK 审批状态。

恢复覆盖 exec_command、write_stdin 函数参数与 apply_patch 自定义工具原始输入，拒绝未准入工具、重复调用身份和命名空间；当前代码不生成用户决定，也不借用普通业务恢复身份。原私有检查点仍不携带旧凭据。

末版 Python 五文件66项通过，0失败/错误/跳过，14.89秒，`.tmp/goal-g4132-memory-approval.xml` 已回读且进程退出0。新增测试使用真实 SDK Runner 中断、序列化、恢复和执行；后端审批回执为测试替身，包含三种工具批准/拒绝/待审批/执行中/已完成/失败和错绑回执，不是 Go 联合验收。首轮测试的样例参数误写已修正后重跑。本轮未修改 Go/前端或运行 Go 行为测试，未部署或操作用户服务/数据。

仍待生产 Worker 自动继续与生命周期、生成来源文件准备、原生工作区恢复引用、阶段提交/整合发布及授权/自动归档排队/用户入口闭环；完整同版回归与全局 review 未完成，已有环境回归阻塞未关闭。Goal 保持原完整范围。

### G4.131 实现与验证记录

G4.131：新增调度 pause 操作，在与既有 checkpoint 共用的事务中完成 SDK checkpoint CAS/配额校验与 status=paused、lease 清空，记录 MEMORY_GENERATION_APPROVAL_PENDING。暂停后执行身份、start/renew 不再可用；只保留当前 worker/attempt 的 token hash 以验证完全相同的暂停回执重读，不赋予执行权。用户 resume/cancel 沿原控制路径清除旧 token hash，新 claim 使用新尝试/令牌。

Sidecar persist_memory_pause 使用实际 SDK RunResult 的原 stage 构建私有检查点，复核 claim 绑定与提取原来源，提交后校验暂停状态、checkpoint hash、无租约、原执行字段和 revision；SDK 已完成结果不能冒充审批暂停，检查点不保存旧凭据。Job 类型支持 paused 回执，但领取与 start/renew 校验仍明确只接受 running。

Go 新增原子暂停、同回执重试、暂停后 start/renew 拒绝、旧令牌失效和新 claim 保留检查点的测试源码；末版 build ./... 与 vet ./internal/runtime ./internal/httpapi ./internal/agenttool 均退出0，未运行 Go 行为测试。Python五文件79项通过，0失败/错误/跳过，19.73秒，`.tmp/goal-g4131-memory-pause.xml` 已回读且进程退出0；包含真实SDK审批中断和真实回环HTTP检查点传输。diff check通过，未改用户数据/服务或部署。

审批决定恢复进 SDK、生产 Worker 自动继续、生成来源文件准备、阶段提交/整合发布与授权/自动归档排队仍未完整连接；完整同版回归及全局 review 未完成，既有环境回归阻塞未关闭。Goal 保持原完整范围。

### G4.130 实现与验证记录

G4.130：生成身份开放 GET /internal/v1/agent-tools/catalog，但 Runtime 在返回前重新校验当前生成租约与来源，只投影 exec_command/apply_patch/write_stdin 描述符，不返回 MCPServers/HostedTools。目录及 Begin 共用生成专用 registry 获取，直接使用操作方注册的原生描述符，不解析个人 MCP 凭据或重编译工作区外部工具配置；当前原生描述符不受 MCP/hosted workspace switches 控制。

Sidecar get_agent_tool_catalog 识别生成上下文，使用已有禁止重定向/代理、严格响应封装的私有传输，GET 响应上限256KiB，并拒绝外部服务配置、未知/重复工具、错误 kind/name/approval/server 绑定。普通业务目录获取保持不变；生成 AgentToolProvider.prepare 可沿原接口取得私有目录，不新建工具执行协议。

末版 Go build ./... 与 vet ./internal/runtime ./internal/httpapi ./internal/agenttool 均退出0；Go 目录/路由测试源码已补，未运行 Go 行为测试。Python四文件61项通过，0失败/错误/跳过，12.40秒，`.tmp/goal-g4130-memory-catalog.xml` 已回读且进程退出0；含真实回环HTTP目录准备、混入外部配置/错误工具拒绝、重定向不转发及原归档/调度传输回归。diff check通过。未部署或操作用户服务、作品和数据库。

还需生成来源文件准备、审批决定恢复、阶段提交/整合发布、授权/自动归档排队及完整生产 Worker 调度；完整同版回归与全局 review 未完成，先前 Windows/MCP 扩大回归阻塞仍未关闭。Goal 保持原完整范围。

### G4.129 实现与验证记录

G4.129：原 NativeWorkspaceExecution.prepare 增加生成路径，不调用 list_workspace_files，也不接受选中的业务 Skill 或私有原生工具范围外的启用描述符。准备与恢复均校验初始 manifest 只能包含私有记忆目录及其真实资源引用，拒绝项目文件、Skill 目录和错路径别名。SDK 返回 Path 使用 as_posix 比较，已修复 Windows 反斜杠导致合法私有子文件被误拒绝的问题。

生成绑定使用单独的私有阶段指令，不再提示项目文件发布、Skill 安装或 Agent 自行发布 Memory；它只生成候选内容，平台仍负责审批与最终持久化。继承业务工具/handoff/MCP 的 Agent 在工作区准备前被拒绝。普通业务路径保持项目文件读取和原发布指令，新增对应分支回归。

末版三文件 27 项通过，0失败/错误/跳过，12.28秒，`.tmp/goal-g4129-memory-workspace.xml` 已回读且进程退出 0。测试使用真实 SDK Manifest/Agent 绑定及资源准备替身，不涉及磁盘、OCI 或真实后端服务；不是原生引擎联合验收。diff check 通过。本轮未修改 Go/前端，未部署或操作用户数据/服务。

还需私有工具目录的生产获取、生成工作区来源文件准备、审批决定恢复、阶段提交/整合发布、授权/自动归档排队和生产 Worker 调度接入；完整同版回归与全局 review 未完成，之前的扩大回归环境阻塞也未关闭。Goal 保持原完整范围。

### G4.128 实现与验证记录

G4.128：新增 run_memory_stage，将实际 SDK Runner.run 接到已确认的 start 回执之后；校验阶段/模型/来源绑定、提取原始输入、恢复 stage hash 与上下文的准确生成身份及 backend。恢复使用已有 restore_memory_stage，不制造审批决定；无已确认决定时仍保留 SDK 中断。RunConfig 强制关闭敏感追踪内容，不另写 Agent 循环。

通过轻量 transport 适配复用既有 NativeWorkspaceLeaseGuard 的取消/清理生命周期。续租任务在调度上下文创建，SDK 模型工具使用独立生成上下文；启动和每次续租确认后检查剩余租约足以覆盖下一次续租周期及请求超时。续租失败取消当前 Runner，转换为不含底层诊断的 MEMORY_GENERATION_LEASE_UNCONFIRMED；外部取消仍按 CancelledError 传播。

末版三文件 41 项通过，0失败/错误/跳过，18.53秒，`.tmp/goal-g4128-memory-execution.xml` 已回读且进程退出 0。新增测试实际调用 SDK Runner，使用无网络模型替身证明 start 前不运行、来源不符拒绝、模型进入后续租失败取消并清理、正常续租、外部取消和短剩余租约拒绝。初版取消测试的注入早于模型进入，已改为事件同步后再注入失败，末版验证通过。diff check 通过。

这是可复用的 SDK 阶段执行入口，仍未接成生产领取循环；返回 RunResult 不代表 durable checkpoint/阶段提交/最终发布完成。原生生成工作区准备、审批决定恢复、阶段提交、授权/自动归档排队、生产 Worker 生命周期以及完整同版回归和全局 review 仍待闭环。前述 Windows/MCP 扩大回归阻塞未关闭。本轮未改 Go/前端、未部署或操作用户服务/数据，Goal 保持完整范围。

### G4.127 实现与验证记录

G4.127：BackendClient 接入类型化 start_memory_generation / renew_memory_generation，继续使用隔离调度通道，不带 delegated activity 头。启动必须收到 started=true，且用户/项目/来源/模型/阶段/尝试/基线/检查点绑定保持一致；租约不可缩短、必须仍有效，状态或到期时间推进必须伴随 revision 推进。续租不得改变 started 或本次提交前的 job 绑定。

续租前另行比较原 claim 的执行身份，允许已经确认的 checkpoint 写入推进 checkpoint hash/revision，而不允许借用另一 generation/attempt 的令牌。新增合法检查点推进后续租和跨尝试在 HTTP 前拒绝的测试，避免把原领取 checkpoint hash 永久当成当前状态。

本轮仅修改 Sidecar 客户端、回执校验和测试。末版三文件 64 项通过，0失败/错误/跳过，23.72秒，`.tmp/goal-g4127-memory-transition.xml` 已回读且进程退出 0；包含真实回环 HTTP 启动/续租身份传输，但不是 Go 联合验收或生产执行。diff check 通过。先前扩大回归中的 Windows 临时目录与 MCP 管道错误仍未关闭。

生产 Worker 尚未实际驱动这些接口，SDK 执行与续租失败后的终止、阶段提交/审批恢复/整合发布、授权和自动归档排队仍未完成；同版完整验收与全局 review 也未完成。没有启动用户服务、部署或改动用户数据，Goal 保持原完整范围。

### G4.126 实现与验证记录

G4.126：新增严格的 MemoryGenerationJob/Claim 契约并接入 BackendClient.claim_memory_generation，沿用原有隔离调度传输。领取前验证 worker/model/lease/policy 配置；空队列返回 None，非空回执验证运行中但未开始的租约、模型、用户/项目/原活动/segment/source hash。私有 token、来源、checkpoint 和 extraction 不进入 repr，验证异常不带原始响应链。

恢复 checkpoint 必须匹配当前可信 policy、模型、阶段、基线与来源绑定，同时复核完整 checkpoint 字节哈希和 SDK state 字符串哈希；具体 SDK 可恢复性与 stage hash 仍由已有 restore_memory_stage 在阶段组装后校验。提取阶段拒绝附带旧提取结果；整合阶段只接收准确三字段、非空且 slug 合法的 SDK 提取结果，不能带空结果推进整合。

本轮仅修改 Sidecar 契约/客户端及测试。四文件针对性回归 68 项通过，0失败/错误/跳过，24.23秒，`.tmp/goal-g4126-memory-claim.xml` 已回读且进程退出 0；包括真实 SDK 原来源构造和真实回环 HTTP 领取响应，未运行 Go 联合验收。diff check 通过。G4.125 的 MCP 管道与临时目录扩大回归错误仍未关闭，本轮绿色报告不替代全量回归。

生产 Worker 的 SDK 执行、续租、阶段提交、审批恢复与整合发布，以及授权/自动归档排队、完整同版验收与全局 review，仍未完成。没有启动生产生成循环或用户服务，没有部署或操作用户数据；Goal 保持原范围。

### G4.125 实现与验证记录

G4.125：继续连接 Worker 所需的现有适配器，尚未启动生产生成循环。AgentContext 增加生成身份字段，规则/记忆快照键支持 memory:；普通业务上下文序列化拒绝生成上下文，继续要求专用私有 SDK checkpoint。AgentToolProvider 原生工具登记传递 generation ID/attempt，并拒绝混合业务身份或未准入工具；本地调用缓存绑定项目/会话/生成/尝试及令牌摘要，不能跨尝试复用。BackendClient 原登记接口发送对应字段，不新增另一套工具执行协议。

生成领取回执增加原 conversation_id，由后端在领取事务内通过原 turn/task/execution 记录和项目范围解析；不从 rollout 模型内容或当前主会话推测。Go 测试源码补会话回执断言；末版 build ./... 和 vet ./internal/runtime ./internal/httpapi ./internal/agenttool 均退出 0，未运行 Go 行为测试。

Python 新增 11 项工具身份、缓存跨尝试拒绝、真实回环 HTTP 头/正文绑定、快照键和私有序列化边界测试，首轮 XML 确认全部通过。扩大回归并未全绿：`.tmp/goal-g4125-memory-integration.xml` 为 47通过/1失败，失败为现有 MCP stdio 创建管道 WinError 5；`.tmp/goal-g4125-memory-focused.xml` 为 81通过/4 fixture错误（另有1项明确未选）；规则回归 14通过/3 fixture错误，均为系统 pytest 临时目录访问拒绝。

尝试全新的项目内隔离 basetemp 后仍收到 WinError 5，进程在清理阶段退出 1；`.tmp/goal-g4125-memory-workspace.xml` 已回读为102项、0失败、7错误、0跳过（另有1项未选），不得作为通过报告。未修改权限/系统策略，也未重复绕过 MCP 管道或 Go 执行限制。diff check 通过，未部署、操作用户服务或修改用户作品/数据库。

生产 Worker、完整领取契约校验、授权与自动归档排队、阶段执行/审批恢复和整合发布仍待闭环；整体同版回归及全局 review 未完成。上述环境测试阻塞与这些可实现代码缺口分开记录，Goal 保持原范围。

### G4.124 实现与验证记录

G4.124：HTTP 接纳独立 Memory generation ID/attempt/token，严格检查必需身份头唯一且非空、尝试号规范正整数、不得混入其他模式头。生成身份使用专用路由白名单，只开放执行快照、私有原生工作区和关联工具生命周期/状态读取；拒绝业务接口、工具目录、用户审批写入、普通产物与 Memory 发布、来源归档和调度接口。生成调用完成不获得普通对话的终态宽限，仍检查当前租约；工具 GET 同样检查当前生成归属。

规则与记忆快照接口补上 memory:activity key；生成工作区 manifest 拒绝项目文件和 Skill 初始资源，只接受已校验私有记忆基线。Sidecar 新增独立 backend_memory_activity 上下文，支持原生工作区传输，禁止借用原执行上下文，退出/异常时复原；既有 transport 的冻结身份比较拒绝跨 attempt 复用。

末版 Go build ./... 和 vet ./internal/runtime ./internal/httpapi ./internal/agenttool 退出 0；新增 Go 身份头/路由测试源码未执行。Python 三文件针对性回归 63 项通过、0失败/错误/跳过、34.80秒，`.tmp/goal-g4124-memory-activity.xml` 已回读且进程退出 0。包含新上下文测试和新增第四执行模式的真实回环 HTTP 租约/快照保存读取恢复；HTTP 服务为测试 fixture，不是 Go 后端或生产 Worker。未修改前端、启动用户服务、部署或迁移用户数据库。

当前还需生产 Worker、生成授权、来源自动归档排队、阶段执行/审批恢复与整合发布闭环；工具目录和发布的生成作用域须随这些调用接入，不能绕开现有白名单。完整同版验收与全局 review 未完成，Goal 保持原范围。

### G4.123 实现与验证记录

G4.123：BeginAgentToolCall 接入生成工具登记，不再统一拒绝生成身份。命令显式绑定 generation ID/attempt/token，与内部服务上下文逐字段比对，拒绝混入 turn/task/execution/Skill 身份。仅允许 extraction/consolidation 阶段的原生 exec_command/apply_patch/write_stdin，且可信目录必须是 runtime_function、always approval；其他业务、发布和外部工具未获准。

新调用在共享工具插入与审批的同一事务中绑定生成作业及原来源会话，保存私有原参数并替换公共摘要。SDK 调用 ID 重试先验证私有归属和当前阶段，普通执行不能复用生成调用，生成执行也不能复用未绑定的普通调用。新增测试源码覆盖真实 Begin 到私有提案、审批前拒绝 Start、同调用重试、身份/参数冲突和跨模式复用；更新原先统一拒绝登记的断言。

末版 Go build ./... 与 vet ./internal/runtime ./internal/httpapi ./internal/agenttool 均退出 0，diff check 通过；未运行 Go 行为测试，静态检查不作为上述行为已经验收的证明。没有修改用户数据库、启动服务、部署或触碰 8860/8880。本轮未改 Sidecar/前端。

HTTP 当前仍不接纳生成身份，生产 Worker 尚未调用此入口；生成授权、来源自动归档/排队、阶段执行与审批恢复、整合发布、最终同版回归和全局 review 仍未完成。白名单登记是主链路连接的一部分，不替代完整能力范围或最终验收。

### G4.122 实现与验证记录

G4.122：前端新增 MemoryToolApproval 并接现有 AgentToolApprovalCard 分流，识别后端私有工具摘要标记，不落回普通允许调用。通过 no-store 私有预览 API 校验用户/项目/调用/参数哈希/审批 ID/version/subject 后展示原参数；未确认前无批准入口，失效提案只允许拒绝。用户、只读权限或审批身份变化会重建状态并丢弃晚到响应；重复点击受锁保护，未知提交结果只能继续原操作，明确 4xx 冲突清除旧参数。使用现有受限宽度/换行样式和图标刷新按钮，未启动浏览器或做视觉验收。

TypeScript 检查通过；针对性三文件 111 项通过。末版完整前端回归 69 文件、1036 项通过，0失败/错误/跳过，252.20秒，`.tmp/goal-g4122-frontend-all.xml` 已回读，进程退出 0。新增组件 17 项及共享卡片分流 1 项覆盖真实 React 交互，但接口为测试替身，未运行 Go 联合验收。本轮没有修改 Go/Sidecar、部署或启动用户服务。

前端源码入口已接，不代表用户当前运行版本已更新，也不代表已能产生真实生成工具审批。BeginAgentToolCall 的生成登记仍关闭，专用工具白名单/策略、HTTP执行身份、生产 Worker、整合与审批发布及生成授权仍待闭环。当前继续原完整能力 Goal；全局 review 和最终验收未完成。

### G4.121 实现与验证记录

G4.121：schema 源码 73 为生成工具关联增加私有 arguments_json，绑定时复核共享审计原参数哈希并纳入配额，公共参数摘要替换为私有标记；忘记操作清除私有参数。生成关联工具的完成结果/trace、失败消息/错误码和取消理由不写入共享私有正文。新增本人 GET `/api/v1/agent-tool-calls/{agent_tool_call_id}/memory-tool-proposal`，响应 no-store，返回准确审批 ID/version/subject、参数哈希和当前有效的原参数。

共享审批在幂等回执之前校验生成工具归属和当前编辑权限；新批准再次复核来源、阶段、作业状态及参数哈希。失效提案不返回参数、不可批准，但本人仍可拒绝；内部服务和 delegated activity 不能代替用户审批，归属不一致记录报错而非落回普通工具审批。Go 测试源码扩展了本人预览、服务拒绝、公共摘要脱敏、阶段变化后的禁批/可拒与正文隐藏。

末版 Go build/vet 均退出 0，未执行 Go 行为测试。本轮没有迁移用户数据库、启动服务或部署，也未修改 Sidecar/前端。生成工具登记依旧关闭，绑定 helper 未从 BeginAgentToolCall 启用；还需工具白名单/策略、前端私有审批入口、执行身份 HTTP/Worker 与发布闭环。当前是能力补齐，不是全平台验收或最终全局 review 完成。

### G4.120 实现与验证记录

G4.120：schema 源码 72 新增 agent_memory_tool_calls，保存 generation、phase、original_attempt，与共享工具记录和作业外键关联；未迁移用户数据库。新增同事务绑定 helper，校验独立已准入身份、项目/工作区及原来源会话，拒绝已有业务执行归属。共享工具启动/完成路径加入生成身份与当前阶段校验，完成/失败/取消的提前幂等返回前也先复核；业务执行不能访问生成关联，生成执行不能访问未关联或上阶段调用。

工具登记入口仍主动返回 unsupported，绑定 helper 尚未接 BeginAgentToolCall。必须先完成私有参数/结果/错误展示、本人审批、工具白名单和策略绑定，再在共享登记事务中启用关联；目前不能声称生成工具已经可用。HTTP 生成执行身份头也未开放。此阶段没有绕过原工具审批、执行回执和配置检查。

末版 Go build/vet 均退出 0；新增同事务关联、跨模式、无执行上下文和旧阶段拒绝的 Go 测试源码，未执行 Go 行为测试。本轮未改 Sidecar/前端，没有服务启动、部署或用户数据库/网关操作。生产 Worker、整合审批发布、来源授权和前端入口仍未闭环，完整 Goal 继续 active，尚未进入最终全局 review 验收完成状态。

### G4.119 实现与验证记录

G4.119：Runtime 内部 AgentActivityIdentity 增加 MemoryGenerationID/MemoryGenerationAttempt，四种身份互斥，校验当前已开始的 running 作业、有效 lease/attempt/token、原来源和当前成员权限；拒绝终态豁免。生成身份使用独立 `memory:<generation_id>` 指令/记忆快照，恢复时如果检查点存在却无原快照则报错，不临时套用新指令。NativeWorkspaceOwner 接入生成作业租约和 token epoch，暂停/失效后不能继续通过旧工作区权限校验。

当前只接 Runtime 内部身份，尚未添加 HTTP 执行身份头。为避免未完成专用审计时误借业务调用，生成身份在 BeginAgentToolCall 明确报 unsupported，ValidateAgentActivityToolCall 拒绝业务关联；后续必须新增 generation/tool-call 审计关系再开放工具执行。没有把此限制当成已完成工具能力。

新增 Go 集成场景测试源码覆盖准入前拒绝、身份/快照/工作区租约、混合模式/错误 attempt/token/跨项目/终态引用、业务工具拒绝、暂停撤权。末版 `go build ./...` 与 `go vet ./internal/runtime ./internal/httpapi ./internal/agenttool` 均退出 0，diff 检查通过；Go 行为测试仍未执行，本轮未改 Sidecar/前端。没有启动服务、部署、修改用户数据库或网关。剩余生产 Worker、专用工具审计及策略、HTTP执行身份、整合审批发布和前端入口仍未闭环；完整 Goal 不标记完成。

### G4.118 实现与验证记录

G4.118：新增 `memory_consolidation.py` 整合输入准备适配器，复用固定 SDK 的原生 raw-memory/rollout-summary 格式化函数、SandboxMemoryStorage、输入选择和 raw_memories.md 汇总。准备前复核来源身份、保存抽取结果哈希/非空契约、Agent 配置及路径/数量边界；来源 JSONL 和两个派生文件逐项写后回读。SDK 选择不得遗漏当前来源、重复输入或引用越界路径；选中正文和摘要须可读，汇总回读须精确匹配选中正文，选中正文总量限制 16 MiB。返回原生整合阶段和选择结果，不写 phase_two_selection.json，不将准备当成已经整合。

新增 9 个固定 SDK + 内存文件系统场景；相关六文件共 87 项通过、0失败/错误/跳过、7.71秒，`.tmp/goal-g4118-memory-expanded.xml` 已回读，进程退出 0。涵盖原内容及 SDK 元数据、重复准备、来源/哈希/参数错配、未配置模型、写入/目录读取/汇总损坏。没有运行真实 Sandbox、Go Worker 或模型网关，本轮未改 Go/前端；结果不是全量联合验收。

仍须生产 Worker 将本适配器接到已授权且版本绑定的私有工作区，完成真实整合 Runner、暂停恢复、结果持久化及审批发布；来源自动捕获/生成授权和前端作业入口也未闭环。固定 SDK 的私有格式化 helper 是显式版本依赖，升级需重验。Goal 仍在能力补齐，不是最终全局 review 完成。未操作用户服务、数据库或部署。

### G4.117 实现与验证记录

G4.117：检查固定 SDK `memory/manager.py` 的整合前文件准备发现抽取提交存在契约遗漏：非空 rollout_slug 还必须通过 SDK normalize_rollout_slug，不能任意字符串。Sidecar 现在在提交前调用原生规范化校验并净化错误，Go 按同规则验证可选 `.md` 后缀、长度、字符集，避免把非法路径/名称排入 consolidation。Go JSON 解析改为结构化逐字段读取，拒绝重复字段、null/缺项/额外字段和尾随内容；空白判断补齐 Python str.strip 的四个信息分隔符，与 SDK 全空结果语义一致。

新增 8 项 SDK slug 场景，末版相关五文件 87 项通过、0失败/错误/跳过、6.51秒，`.tmp/goal-g4117-memory-contract.xml` 已回读。Go 编译和静态检查通过；新增 Go 对应契约测试源码，未执行 Go 行为测试。此次为整合接入过程中发现的问题修复，不是全局 review 或整合执行完成。生产 Worker、整合文件准备/执行/审批发布和前端入口仍未闭环。未操作用户服务、数据库、部署或真实网关。

### G4.116 实现与验证记录

G4.116：抽取阶段提交及持久化转换已接。schema 源码升至 71，为 generation 增加私有 extraction_json/extraction_receipt，未迁移用户数据库。内部 extraction 操作要求独立 Worker、有效且已开始的抽取租约、当前权限和来源，验证 SDK 三字符串结果及全空/非空契约，部分为空拒绝。事务中保存结果及原 attempt/worker/token 哈希回执；有内容排入 consolidation、清空旧检查点并撤销旧令牌，无内容正常完成而不创建 Memory 版本。原字节与原身份可重读提交回执，不重放抽取。领取整合阶段时携带已验证的抽取结果；正文/回执损坏拒绝，配额和忘记清理覆盖新增内容，项目删除随 generation 同行删除。

Sidecar `persist_memory_extraction` 以原生 `RolloutExtractionArtifacts` 验证并提交，严格核对 generation/content_hash/has_memory 回执。相关五文件 79 项通过，0失败/错误/跳过，10.87秒，`.tmp/goal-g4116-memory-extraction.xml` 已回读；包括原生阶段、检查点、来源/Worker HTTP 及抽取回执四个场景。末版 Go build/vet 均退出 0；新增 Go 阶段转换/空结果/原回执/忘记与无效契约测试源码，但未执行 Go 行为测试。没有操作用户服务、用户数据库、部署或真实模型网关。

仍未接通生产 Worker 的模型执行、来源捕获和自动生成授权、可信工具/模型配置绑定、整合结果持久化与审批发布闭环、前端作业入口。此次只完成抽取后的 durable transition，不把单阶段通过当成自动 Memory 或全平台完成。Goal 仍 active；同版全量验收和最终全局 review 未完成。

### G4.115 实现与验证记录

G4.115：Go 内部 POST `/internal/v1/agent-memory/generations/{operation}` 接入 claim/start/renew/checkpoint，要求内部认证及独立 Worker，显式拒绝 delegated activity，未支持的操作返回 404。请求体普通 4096 字节、检查点 4 MiB + 4096 字节，响应 no-store；空队列返回 `{data:{claim:null}}`，其他操作返回 job 元数据。补充不存在作业的领域错误映射，避免 Worker 查错 ID 变成 500。此接口没有直接完成或发布操作。

Sidecar `BackendClient.memory_generation_request` 仅允许上述四条固定路径；拒绝普通执行上下文，不附带旧对话的 X-Agent 身份，复用私有 Memory 严格 HTTP 传输边界，禁代理和重定向、限定响应大小、净化错误、不自动重试，并检查 claim/job 返回封装。领取得到的具体身份、来源及配置还需生产 Worker 做跨字段验证，本方法不能代替执行准入。

新增 14 个真实本地 HTTP 传输场景，与来源传输和原生检查点共 56 项通过、0失败/错误/跳过、8.61秒，`.tmp/goal-g4115-memory-worker-transport.xml` 已回读；末版 Go build/vet 均退出 0。Go HTTP 测试源码新增普通用户拒绝、空队列、未知作业/操作，但未执行 Go 行为测试。没有启动用户服务或运行迁移/部署，未修改网关和凭据。

仍缺生产执行循环、可信模型/工具策略绑定、来源捕获与自动生成授权、阶段输出持久化/转换、发布闭环及前端作业入口。当前是 W5 能力补齐，尚未达到同版全量验收和最终全局 review 完成条件。

### G4.114 实现与验证记录

G4.114：新增 `memory_checkpoint.py`，针对暂停审批的原生 Memory 阶段调用 SDK `RunResult.to_state` / `RunState.to_json` 保存并通过 `RunState.from_json` 恢复，不把检查点当提示词重新生成。不可变封装绑定 generation/project/user/phase/source/base Memory/model/policy；复核原 Agent、原输入、静态指令、模型参数、工具名称/Schema/审批形态、输出 Schema 和工具选择策略，校验状态摘要及 4 MiB 持久化上限。私有 runtime context 清空后要求新 Worker 显式提供 context override，保留 SDK 审批和调用状态。policy_hash 必须由可信 Worker 配置提供；回调实现、模型对象配置等仍需未来 Worker 的绑定约束，不能仅靠本模块证明完整执行隔离。

新增 14 个原生 SDK 测试：两个阶段的批准/拒绝、序列化回读后精确一次工具执行、上下文凭据替换、九种作业绑定错配、输入/指令/工具/输出/模型参数改变、损坏状态和缺失新 context。末版相关五文件共 59 项通过，0失败/错误/跳过，3.50秒；`.tmp/goal-g4114-memory-approval.xml` 已回读。均使用固定 SDK 和本地模型替身，无 Go Worker/用户服务/真实模型联调；本轮未改 Go 或前端，没有将前轮 build/vet 作为本轮功能验收。

当前适配器只支持 SDK 已暂停审批的恢复，拒绝运行中流和已完成结果；一般异常/中途崩溃的恢复、来源生产捕获、授权设置、专用执行身份、生成 Worker、阶段转换/发布闭环及前端作业入口仍未接通。当前仍为 W5 能力补齐，不代表同版全量验收或最终全局 review 完成。

### G4.113 实现与验证记录

G4.113：补充 Memory generation 的续租、用户查询和暂停/取消/恢复控制。续租要求当前独立 Worker、有效 attempt/token、当前成员权限与原始来源有效；不会缩短租约或复活过期租约。用户控制按项目与本人归属先查找再读取，要求最新 revision；暂停/取消立即撤销旧令牌。已开始但无保存检查点的作业禁止恢复，恢复重新校验来源和 Memory 版本，重新领取保留原检查点、绑定模型并轮换 attempt/token。直接用户入队也明确拒绝 delegated activity。

用户 GET `/api/v1/projects/{project_id}/memory/generations/{generation_id}` 和 POST 对应 `/control` 已接 Store，响应 no-store，JSON 不包含私有检查点、Worker 或令牌；不存在/其他用户作业统一 404，控制体上限 4096 字节。尚未接前端列表/操作入口，也尚未接生产 SDK Worker；此处的恢复只表示调度状态允许重新领取，不等于 SDK 状态恢复执行已完成。

末版 `go build ./...` 与 `go vet ./internal/runtime ./internal/httpapi ./internal/agenttool` 均退出 0，目标文件 diff 检查通过。新增两个 Go 生命周期测试及现有 HTTP 测试的五个接口场景源码，覆盖续租、检查点恢复领取、旧令牌隔离、取消、权限及 404；依既有环境限制未执行 Go 行为测试。没有操作用户服务、数据库、部署、浏览器或模型网关。

当前剩余仍包括生产来源捕获、自动生成授权、专用执行身份与 SDK Worker、真实 SDK 检查点恢复、阶段转换及发布闭环、前端作业入口，之后才是同版验收及完整全局 review。仍属能力补齐，不标记 Goal 完成。

### G4.112 实现与验证记录

G4.112：Memory generation 新增持久化作业表（schema 源码 70，未迁移用户数据库），用户显式入队、独立服务 Worker 领取和模型开始前校验。领取时重新解析用户成员权限、原始来源及当前 Memory 版本；未开始的过期租约可重新领取，已经开始的作业暂停而不盲目重放。模型绑定变化暂停，损坏检查点隔离为失败，撤销来源取消。忘记操作清除来源正文、作业检查点和令牌；作业纳入配额和项目删除处理。

本轮继续加入原生 SDK 状态的私有检查点保存：上限 4 MiB、JSON 合法性、已开始的有效 Worker 租约、当前权限与来源复核、前一检查点哈希 CAS、完全相同字节的幂等重试、增长配额校验。不会自行改变阶段或将模型状态视为 Memory 已发布。新增 Go 测试源码覆盖入队/三身份权限、租约过期、来源撤销及检查点准入/CAS/重试，但未执行 Go 行为测试。末版 `go build ./...` 和 `go vet ./internal/runtime ./internal/httpapi ./internal/agenttool` 均已退出 0；没有将编译/静态检查记为行为验收。

仍未实现：生产执行的来源捕获调用、自动生成授权设置、生成 Worker 与专用执行身份、检查点恢复/续租/阶段转换、发布闭环及用户作业入口。当前仍在 W5 能力补齐；不能据此宣布全部 SDK 功能、同版验收或全局 review 完成。原生 Responses compaction 的网关阻塞仍独立记录，不以本地摘要替代。本轮没有运行服务、迁移数据库、浏览器验收、部署或修改用户作品。

### G4.111 已有验证记录

G4.111：私有SDK来源的Sidecar保存/回读transport已接。`BackendClient.memory_rollout_request`仅允许两个固定内部端点，必须携带原执行身份；禁用隐式代理和重定向，校验响应类型/体积/长度/重复头及非空data，错误只保留状态码和规范错误码，不回传私有错误正文。`persist_memory_rollout`复核原来源后发送已冻结记录并严格校验回执；`restore_memory_rollout`要求原分段和预期哈希，复核归属/原记忆版本与正文，不把新生成时间戳或其他版本当成原请求恢复。没有默认自动重试。

专项4文件69项通过、0失败/错误/跳过、17.13秒，`.tmp/goal-g4111-memory-transport.xml`已回读；包含三种执行身份的真实本地HTTP、错配保存/回读、未知保存后原字节重试及读取、代理/重定向与响应边界。末版完整SDK回归1771项通过，0失败/错误/跳过、473.05秒，`.tmp/goal-g4111-sdk-all.xml`已回读，进程退出0，全部检查进程终态。使用固定SDK和本地模型/HTTP/后端/引擎替身，不是实际Go或网关联合验收。临时目录为上一轮已核验的仓库专用目录，经审批运行；本轮未改Go或前端，G4.110的build/vet与G4.109的前端数字不能当作本轮全量。

当前源捕获、保存与提取入口仍需要生产生命周期调用者和持久调度串联；完整服务重启恢复、提取/整合批准发布、自动生成启用与用户可见状态尚未接通。已读native_runner确认主提交会先关闭原生工作区并暂停租约，不能在终态后用旧执行身份开启生成工具，也不能把内存中的待上传对象冒充持久恢复。下一动作必须补持久生成执行及来源调度，而非只增加提示或让普通Skill后台队列伪装为Memory。原范围、自测联合、最终全局review保持未完成，未部署或操作用户服务/数据。

G4.110：SDK生成来源合同与Go私有归档补源码。`memory_rollout.py`从实际SDK RunResult使用原生rollout序列化，绑定执行/用户/作品/原记忆版本及分段ID、正文哈希，冻结后不受原结果对象后续修改影响；拒绝未终态流、成功与异常矛盾、错来源及损坏正文，不保存原始异常消息。`memory_extraction_from_source`复核来源后使用原生提取提示与输出Schema，不新建模型循环。

Go新增`agent_memory_rollouts`及内部保存/读取端点，原执行用户/记忆版本校验、私有幂等来源、配额、删除预览、项目/工作区删除和遗忘正文清理。来源接口可在原执行终态后归档，但事务再次核对当前权限和记忆撤销；不授予执行新工具或发布记忆权限。初始未读记忆的执行也不能在用户后续编辑/遗忘后重存旧来源。Schema源码69，未运行迁移。

来源/阶段专项34项通过；扩大后首次临时目录WinError5导致4个夹具错误，改仓库专用目录仍受限，经审批在同一已核验目录重跑，末版4文件52项通过、0失败/错误/跳过、6.03秒，`.tmp/goal-g4110-memory-expanded.xml`已回读。Go全包build通过；末次来源终态门禁调整后HTTP/runtime/agenttool vet通过，Go三模式新建/已有记忆共6个归档/幂等/身份/遗忘场景源码未运行。全部进程终态，未访问用户服务、数据库、模型、网关、OCI或部署。

当前仍未接Sidecar来源上传/回读transport、生产捕获调用、提取/整合的持久调度与恢复；原生归档合同不能冒充自动Memory完成。现有Skill后台队列不可直接当作独立Memory作业，后续须贯通完整用户链路而非只累积模块。跨会话未知结果核对、旧原生快照隐私清理、真实联合、W0-W9与最终全局review保留。用户询问剩余工期，当前没有足够证据给可信总时长，不再将专项测试数量作为收尾依据。

G4.109：补私有记忆审批前的候选预览。私有发布意图新增原参数JSON与原工作区会话引用，Schema源码68，迁移仅补空列，不从共享摘要猜旧选择；旧意图不可批准、仍可拒绝。准备登记、本人预览与最终发布共用原不可变快照的文件选择/完整性/正文校验；审批事务再次校验版本与候选。本人GET返回原工具/审批版本/subject/参数哈希、当前记忆和候选文件；他人拒绝访问，用户修改/遗忘或快照不可用后不返回候选正文。配额包含私有参数实际字节，删除预览计入会话引用，未执行数据库迁移。

前端新增私有记忆专用审批卡，绑定当前用户、作品、原调用和审批subject，逐文件显示新增/替换/删除/不变；只读及其他用户不提供可执行保存，未知响应只允许原操作重试，明确冲突清空候选并要求刷新。沿用原审批请求ID/回执处理，没有新建执行循环。专项4文件38项通过（11.16秒，`.tmp/goal-g4109-memory-ui.xml`）；TypeScript、Go全包build、HTTP/runtime/agenttool vet及diff检查通过。新增Go三模式新建/已有记忆共30个发布/预览/编辑/遗忘/旧意图/损坏快照场景源码，未运行Go行为测试。

完整前端68文件1018项通过，0失败/错误/跳过、226.61秒（`.tmp/goal-g4109-frontend-all.xml`已回读），进程退出0。之后仅补记忆卡长文件名换行、单列最小宽度与刷新图标固定尺寸；末版受影响4文件38项再次通过、13.62秒（`.tmp/goal-g4109-memory-ui-final.xml`），TypeScript通过。准确区分全量所在版本与末次样式后的专项，不将jsdom结果当作浏览器视觉验收。检查进程均终态。

本轮未改Sidecar生产代码或SDK依赖，不使用G4.108的1724项SDK结果冒充本轮Go或前端验收；未访问用户服务、数据库、模型/网关/凭据、OCI或浏览器，未部署。下一项仍为SDK原生提取/整合生产调度与持久恢复、跨会话未知结果核对、旧快照隐私清理；真实联合、W0-W9原范围及最终全局review未完成，Goal active。

G4.108：私有记忆发布链路已接源码。Go增加仅元数据的发布意图/回执表、当前执行/用户/原参数审批绑定、本人批准与CAS；从原生不可变快照读取完整私有文件选择，复用记忆版本插入并原子登记回执，不写共享项目文件。当前执行自己的已确认发布不会撤销它的原输入版本；随后用户编辑/遗忘仍撤销旧执行，含起初没有读取记忆但后来发布记忆的执行。未有持久回执不能完成工具。共享工具参数/结果/错误隐藏私有路径与正文；项目删除预览及项目/工作区删除、配额计入意图/回执。Schema源码67，未执行迁移。

SDK新增prepare_agent_memory_publication/publish_agent_memory并接入主对话、后台、状态化工具集，需原生工作区、原SDK调用、本人审批、无自动重试。读取快照区分固定输入version和写入用current_version；保存回执严格校验用户、作品、会话工作区、工具调用、版本及内容哈希。生成正文不放进工具返回；管理弹窗仍从原私有GET读取保存版本。自动提取/整合调度尚未接通，不以这条文件保存链路宣称自动Memory完成。

专项76项通过；全量首轮1722通过/2失败，失败为旧状态化测试的native-only清单未包含新增两个工具。补全清单并保留未启用原生工作区时不可见的反向断言，状态化/保存专项48项通过（36.88秒）。Go全包build和HTTP/runtime/agenttool vet通过；Go新增6个三模式新建/已有记忆发布、本人审批、未持久拒绝完成、幂等回执与遗忘撤销场景及参数反例源码未运行。初次Go vet发现测试命令字段命名错误，已按实际合同修正后通过。

末版全量SDK回归1724项通过，0失败/错误/跳过、227.60秒，`.tmp/goal-g4108-sdk-all.xml`已回读，进程退出0；全部检查进程终态。先前1703项是G4.107，不冒充本版验收。本批仍使用固定SDK与本地模型/后端/引擎夹具；目录WinError5后经审批在原隔离目录重跑。未操作用户服务、Go行为执行、数据库、网关/凭据或部署。下一步生成阶段生产调度、审批可见性/私有候选预览及未知结果核对、旧快照隐私清理，原完整范围与最终review未完成。

G4.107：原生Memory读取已进入三模式共用NativeWorkspaceExecution生产入口。先向Go解析私有执行引用，再将memory作为第三类版本化manifest来源，正文仅通过原执行身份的物化读取接口传输，SDK manifest/checkpoint保存来源而非正文。恢复时比对当前引用与原manifest；项目文件不能占用`.agent-memory`，前后端发布参数都拒绝直接将该目录导出为共享项目文件。SDK Memory使用原生摘要读取、live_update=false/generate=None；禁用/只读策略不额外授予Shell写工具。默认生成钩子仍不启用，完整自动Memory未完成。

三生产流入口读取及下次Runner工作区恢复的专项15项通过；新增Go三模式版本读取/遗忘撤销与直接发布拒绝测试源码，但未执行Go行为测试。Go全包build、HTTP/runtime vet及diff检查通过。隔离临时目录初次WinError5，经审批在原路径运行；测试夹具重复建目录、旧文件清单/权限判断遗漏已修正，未绕过生产安全检查。

末版SDK全量1703项通过，0失败/错误/跳过，218.73秒，`.tmp/goal-g4107-sdk-all.xml`已回读，进程退出0。固定SDK、模型/Go/引擎替身和本地HTTP/文件夹具，不是真实Go/OCI/模型/浏览器联合。未迁移数据库、改依赖/模型/凭据或部署。

下一项：SDK提取/整合阶段的持久任务及私有记忆发布，当前原生读取不可冒充自动生成/保存完成。旧工作区快照正文清理、撤销期间资源回收、对话动作副作用的事务边界、前端跨会话未知请求恢复继续保留；目录直接发布禁令不等于任意生成内容的完全防泄漏。W0-W9原范围、最终全局review及真实验收仍未签收，Goal active。

G4.106：新增三执行模式私有记忆版本引用及内部snapshot接口，引用表不复制正文；按执行权威用户解析，旧检查点/禁用记忆不在恢复时注入。共享执行身份校验拒绝已读版本变化，终态工具收尾仍可进行；主对话Begin/Complete提交、后台完成、状态化结果接收/正式提交与模型恢复增加事务内版本校验，防止只在HTTP入口检查。Schema源码66，项目删除预览包含引用，项目/工作区删除清理引用。未运行迁移或用户服务。

SDK后端客户端新增内部读取方法，三模式原身份/令牌及缺失回执测试；空回执被通用解析接受的反例已通过本接口严格data对象检查修复。首轮异步测试未按仓库asyncio.run约定导致7项未执行，修正后发现2项回执反例，再修复；末版5文件49项通过，0失败/错误/跳过、2.952秒，`.tmp/goal-g4106-memory.xml`已回读。Go全包build与HTTP/runtime vet退出0，新增Go三模式撤销、禁用/旧检查点、提交中途遗忘测试源码未运行。全部进程终态。

下一项仍为原生Memory读取/生成生产调用及持久交付。当前客户端没有生产调用者，不能把引用与检查标为SDKMemory生效；共享入口校验不等于所有工具副作用事务均已覆盖，原工作区发布/动作分发、旧快照隐私清理及生成CAS仍需逐条核验。前端跨会话原请求恢复、真实Go/OCI/模型/浏览器联合与原范围最终review均未完成；未部署，Goal active。

G4.105：Memory用户GET/PUT及工作台弹窗已接。URL作品与当前用户绑定、私有no-store、原request_id回执；文件编辑/增删、启停、确认遗忘及未知回执锁定原请求。8项弹窗、工作台与API扩大248项通过（32.70秒，`.tmp/goal-g4105-memory-expanded.xml`），TypeScript、Go全包build和HTTP/runtime vet通过。首轮jsdom原生dialog方法缺失，补测试适配后通过；新增HTTP真实路由测试源码未运行Go测试，所有进程终态。未访问用户环境、未迁移/部署。

下一项SDK三模式记忆版本引用与撤销检查、原生Memory读取及阶段生产调用。管理页面只操作存储，不能据此宣布自动记忆生效；旧执行快照防复活、跨会话原请求恢复及真实Go/OCI/模型/浏览器联合验收仍需完成。原范围及最终全局review保留。

G4.104：Memory持久层落地到Go源码。新增用户私有项目多文件记忆版本、完整性校验、幂等回执、CAS及遗忘墓碑；遗忘清除本存储历史正文，旧回执不再返回被遗忘正文、旧版本写入冲突。Schema源码65注册迁移，工作区配额计入记忆JSON，项目删除快照包含版本身份，项目/工作区删除清理。新增行为测试源码但没有运行Go测试（现有系统边界保留）；全后端build与runtime vet退出0，所有进程终态，未运行迁移/用户服务/部署。

下一步HTTP用户读写/遗忘和前端入口，之后SDK版本引用/撤销检查及阶段生产调用。当前存储墓碑不能单独阻止未接入的执行快照恢复，完整SDK读取/生成/持久交付/遗忘仍未通过；不以新源码或build替代真实行为验收。G4.103的42项为此前SDK适配专项，本批未改SDK，不冒充Go测试证据。

G4.103：新增SDK记忆阶段装配`native_memory.py`，复用固定SDK原生提示、提取Schema/校验与整合输入；使用平台明确模型和工具配置，保留guardrails，拒绝业务handoff和未冻结动态规则，不创建默认Memory关闭manager。实际SDK Runner与平台审批包装器验证两阶段批准/拒绝、序列化重建及身份保留，模型失败原样向外传播；扩大原审批/参数绑定回归，4文件42项通过、3.38秒，JUnit `.tmp/goal-g4103-memory-expanded.xml`，进程终态。无真实模型/Go/OCI，未修改依赖。SDK内部memory模块导入为明确固定版本兼容边界。

装配器目前没有生产调用者，不把它标为Memory能力完成。下一步实现持久版本与读取/发布/遗忘合同，再接受管任务、原生工作区及用户入口；必须验证真实Shell审批而非仅Function替身。原范围、W5/W8与最终全局review仍未完成，未部署。

G4.102：新增固定SDK Memory契约测试，3项通过，2.33秒，JUnit `.tmp/goal-g4102-memory-contract.xml`。验证原生摘要读取、关闭生成提取失败被日志处理且再次flush不重试，以及整合Agent默认工具未配置平台包装器。首轮弱引用替身错误已修；无真实模型/工具执行，不是平台Memory完成测试。源码确认自动manager新建RunConfig仅带session，MemoryGenerateConfig无自定义Agent/工具钩子参数。下一步验证原生提取/整合原语的受管执行适配，保持SDK逻辑；生成可用前不接自动关闭钩子。完整持久版本、读取/发布/遗忘及用户入口保持待做，未部署，Goal active。

G4.101：Sandbox Memory核对从搜索推测推进为确切生产缺口：native_capabilities仅装配Filesystem/Shell/Skills；固定SDK Memory有独立读取及关闭时提取/整合流程。已读Memory、manager、phase_two、native_runner/native_execution并运行无网络配置探针，确认默认两模型、关闭钩子和依赖。直接追加Memory()不能满足平台审批身份、持久交付及失败确认，generate=None也不能代替完整生成需求。完整证据/接入条件/验收列表见`docs/agent-platform-memory-integration-audit.md`。本轮没有生产修改或新增应用测试通过数，未调用真实模型/环境，非能力完成。

下一动作：验证固定SDK公开生成扩展点如何保留平台工具审批、执行上下文及错误传播，随后实现记忆持久版本/用户操作合同并贯通SDK读写生成；不修改site-packages，不擅自切模型，不将规则/Goal/Session当作Memory完成。W5/W8及完整Goal继续未完成。

G4.100：确认项目Goal由Go持久保存、主SDK通过inspect_project_goal/goal_update管理，但前端未消费快照goal字段。新增工作台可折叠的当前目标/完成条件/版本显示，沿用原投影刷新，不添加目标执行器或另建管理API。组件拒绝非当前作品/会话、非active及不完整字段，权威快照返回null时清除旧目标；管理仍通过原用户显式对话链路。本批5个实际App/API替身新增用例通过，扩大工作台与事件回归225项通过、0失败/错误/跳过，36.43秒，JUnit `.tmp/goal-g4100-workbench.xml`。TypeScript与diff检查通过，所有进程终态。

本批为前端展示增量，无Go/SDK/用户环境修改；G4.99的986项是此前全量，不冒充本版全量。未获浏览器授权，视觉/响应式及真实Goal建立修改完成取消联合验收未执行，不签署完整Goal/W5。下一核对规则与Sandbox Memory的原需求及生产接线：规则已有页面，但不能用它代替SDK原生Memory；初步源码检索未找到Memory类型接线，需读实际native_runner及固定SDK再判断，不能只凭检索宣布功能已齐全或完全缺失。

G4.99：进入W5，确认配置卡将所有非json_schema视图强制判为需要上传材料，导致none视图即使输入Schema不要求材料仍不能保存。扩展现有动态工作流测试后复现一项失败、三个对照通过；现none同样按冻结输入Schema判断材料要求，缺Schema仍保守阻止，原剧本/续写材料规则不变。none标题改用Skill名称，不再错误显示设置剧本体量。配置/投影专项79项通过，TypeScript退出0。最初专项命令列出的JsonSchemaForm.test.tsx不存在，实际仅运行两个文件，不计为第三个覆盖文件。

末版完整前端66文件986项通过，0失败/错误/跳过，254.60秒，JUnit为`.tmp/goal-g499-frontend-all.xml`，测试进程已终态。本批没有修改Go/SDK，未重跑其测试。前端证据为组件/API替身，不等于真实浏览器/Go/网关/OCI验收；未部署或操作用户服务、数据。

W5已读服务端workspaceview/HTTP投影、前端可信视图、配置卡、Skill管理/工具配置/文件与产物入口。后续具体核对项目Goal：服务端快照已有goal、SDK inspect_project_goal和goal_update已有接线，但前端仅声明goal?: unknown，尚无直接显示消费；需核对对话建立/修改/完成/取消的反馈及持久状态可见性，再决定缺口修复，不把没有单独页面直接等同功能未实现。W5及W6-W9、真实联合验收与最终review继续保留，Goal不标记完成。

G4.98当前更新：W4取消/未知结果/人工核对链路核查。Go取消保留started_at；uncertainExternalCallsTx纳入已开始未完成的MCP写入，validateExternalToolOutcomesTx在提交事务内检查待处理调用及原执行核对事实，prepareExternalToolResumeTx只允许原用户明确继续并复验原SDK检查点。以上是源码证据，未运行Go行为验收。本轮发现前端保存核对结果只比快照和请求ID，忽略结论/依据/确认人及原执行身份；六个反例修复前失败、原六项通过。现逐项核对返回事实及工具/参数/配置/用户/执行身份，不匹配保留表单和原键重试，避免把applied错配成not_applied后显示成功。

末版前端三文件19项通过，0失败/错误/跳过，总耗时13.61秒；`.tmp/goal-g498-outcome-ui.xml`已回读。TypeScript检查退出0，全部进程终态。组件/API为替身，非浏览器/Go真实联合；Go/SDK/依赖/schema未改，不重复其回归，不把G4.97 SDK1663项当作本批前端验收。本轮W4所查路径无其他已定位代码缺陷，后续进入W5服务端Projection与用户入口核查；真实W4验收和最终review仍未通过，Goal active，未部署或修改用户数据。

G4.97验证收口：末版完整SDK回归1663项通过、0失败/错误/跳过，JUnit时间276.399秒；日志`.tmp/goal-g497-sdk-all.log`、XML`.tmp/goal-g497-sdk-all.xml`已核对，进程退出0。使用获批仓库隔离临时目录及固定SDK/替身依赖，含本地MCP/HTTP夹具；非真实Go/网关/OCI/用户页面联合验收。diff检查通过。下段“运行中”为本批开始记录，现已终态。当前已读MCP未知结果标记和SDK原生暂停/用户核对恢复测试；下一步核对对应Go取消/未知结果门禁及前端操作闭环，然后进入W5，不重复本批已完成缓存及交付校验。全部目标仍未完成，不签署最终review，不部署。

G4.97当前更新：确认Go BeginAgentToolCall已有相同SDK调用ID的工具/参数冲突检查，但Sidecar缓存命中可跳过该检查。三模式Function审批后换参、Function/MCP/Hosted缓存换工具/值/JSON类型/嵌套内容及旧缓存重读共16个反例修复前失败。共享ensure_call增加工具ID与排序JSON参数的SHA-256绑定，拒绝NaN编码；不以Python字典相等混淆true/1，也不保存额外原文副本。原脚本精确参数/版本策略保留；旧缓存无绑定时重新登记读取Go原回执，而非直接信任或重放工具。两个专项文件38项通过、3.67秒，完整SDK回归运行中（日志`.tmp/goal-g497-sdk-all.log`，JUnit目标`.tmp/goal-g497-sdk-all.xml`）。Go/前端/schema/依赖未改，未部署，原范围和最终review未完成。

G4.96当前更新：W4重读Function包装、MCP三传输/凭据绑定及Hosted文件交付。确认Hosted图片和代码文件调用共享保存函数时漏传project_id/source_type，原本只验证资产/快照配对，未验证当前作品、来源和确切工具调用。两个Hosted类型共八个错配反例修复前全部失败、两个合法对照通过；现在传入当前作品及hosted_tool来源，复用已有调用元数据校验。测试夹具补齐Go正式回执已有的字段；Go/前端/依赖未改。

末版三个工具交付文件79项通过、0失败/错误/跳过、34.095秒，`.tmp/goal-g496-delivery-reviewed.xml`已回读，进程退出0。包括实际SDK Hosted工具/模拟Responses传输、三身份本地stdio MCP输出，提供方和后端为夹具，不是Go/真实网关/OCI联合。首轮67通过/12个默认临时目录权限错误，经审批改用仓库核定隔离临时目录后完整通过，未改安全策略。未重复SDK全量，历史1632项不代表本版全量；未部署或操作用户环境。

W4下一待验证点：`ensure_call`缓存命中核对_configuration_hash，仅脚本工具额外核对原参数。须检查其他工具同一SDK调用ID携带不同参数时，现有审批/恢复/后端检查是否确实阻止执行；目前为具体待证风险，不提前宣称漏洞已复现或已修。原范围、真实联合验收和最终review仍未完成。

G4.95当前更新：完成W2作者草稿/安装/前端未知回执的本轮源码核对，未另发现新增缺陷；继续W3三模式关联与恢复入口，发现后台输出及artifact_draft外壳默认忽略额外字段，会丢弃错误层级的正文并提交成功。实际Worker入口四个反例在修复前均失败，合法开放字段对照通过；现两层外壳extra=forbid，result/payload内容仍原样保留。结构化与JSON兼容模式均覆盖，不新增模型循环或自动重放工具。

末版后台四文件34项全部通过、0失败/错误/跳过、7.098秒，JUnit `.tmp/goal-g495-background-reviewed.xml`已回读，进程退出0。覆盖输出、审批重建、暂停/追加输入及JSON回退；使用固定SDK和模型/服务替身。首轮14通过/20个临时目录权限错误，指定仓库隔离目录后同样遭WinError5，经自动审批在同一目录重跑成功，未改系统策略。没有重跑不受影响的SDK全量、Go或前端；1632项是G4.93历史版本证据，不能冒充本版全量。完整W2/W3验收仍缺Go/真实环境联合，下一项进入W4；未部署，Goal保持active。

G4.94验证收口：使用仓库既有Go工具链及.gocache/.gotmp，全后端`go build ./...`和Runtime包`go vet ./internal/runtime`均退出0，gofmt完成，两个进程均终态。初次PATH未找到go/gofmt，未执行检查；未改系统配置。新增Go行为测试仍未运行，不以build/vet代替通过。本批SDK及前端未改，不重复运行其已完成分层套件。

G4.94当前更新：恢复原Goal后确认G4.93进程句柄已不存在，完整日志到达100%，JUnit XML为1632项、0失败/错误/跳过、279.224秒；因此上次SDK回归已结束，不再视为运行中。该证据仍为固定SDK与替身依赖，不能替代Go/真实网关/OCI联合验收。进入W2后检查目录与ZIP隔离导入、不可变归档、活动目录切换和安装/生命周期原回执。发现完整路径去重漏掉父目录大小写别名及文件/目录冲突；共享路径登记现记录隐式目录，并在写入登记前拒绝两类冲突。新增ZIP目录预检十场景源码覆盖两种顺序和合法兄弟路径；Go行为测试未执行，静态检查待收口。未部署、未操作用户服务或数据。

G4.93当前更新：继续用户工期询问后的原Goal。修复同名不同ID脚本快照覆盖，允许集与版本快照按capability ID保存，SDK脚本工具新增确切ID参数；旧name/script组合唯一才兼容，省略或显式空字段不改变原审批参数哈希。动态激活/升级不改已有调用原参数；Go登记、重试、执行核对ID与固定快照。新增反例修复前11失败/2通过，初修21通过，扩大9文件218通过、146.69秒；末版增加三执行身份审批恢复矩阵，完整SDK回归运行中。Go全后端build/vet通过，新增一项顶层八场景Go行为源码未执行；前端/schema/依赖未改，未部署。仍不能提供可靠整体交期，原逐项核对、真实联合验收及最终review未完成。

G4.92当前更新：W1接线核对发现名称子串直接选中绕过SDK意图判断、未选中inline发现未遵守禁止隐式调用声明。现名称只用于候选收集和排序，意图仍由SDK路由决定；明确菜单引用不增加路由调用，路由失败不回退字符串命中。渐进目录补路径及调用策略，提及项不因普通目录预算被挤掉。共享策略用于动态发现与本地元数据映射，已批准安装和明确选中仍可用。修复前9失败/1通过，8文件扩大回归经正常临时目录/本地夹具审批后179项通过；最后固定源码SDK全量1610通过、0失败/错误/跳过、256.99秒，全部本批进程终态。另确认同名不同capability ID的脚本快照覆盖，须继续修复。Go/前端/依赖未改，未部署；原范围、真实验收和最终review未完成。

G4.91当前更新：按固定W0核对Golden、Skill包夹具、合同和发布门禁；合同反例40/40通过。确认路由评测校验器接受错误字段类型、未固定版本和虚假覆盖标签，先复现20失败/3通过，修复后扩大至30/30通过。该模块是合成路由评测，不是Skill真实执行。Stage8的99/99仅为计划分配校验；发布脚本仍要求真实外部门禁，不存在据旧计划自动发布的已确认缺陷。本批未改Go/前端/SDK依赖，未运行真实环境或部署，全部命令终态。W0真实推进/网关验收仍未过，下一项进入W1，完整能力和最终review未完成，无可靠整体小时承诺。

G4.90当前更新：补前端当前身份刷新、会话失效后的登录恢复和旧请求隔离。返回可见页面、路由切换、可见状态每30秒及事件流撤权控制会复查身份；同一用户角色变化不重建工作台，临时网络故障不冒充退出。明确401/403退出，不删除已登记的未知提交；新登录即绑定Cookie预期，不能在Cookie失效后静默降为本地隐式身份。首页只读写入口禁用/隐藏，旧账号或旧权限创建、上传、批次接续、重命名和删除回执不再触发后续写入/导航。初始11个身份反例均失败后修复；新增首页反例和新登录Cookie反例另有修复前证据。末版专项22项及TypeScript/隔离Vite通过，最后固定源码全量66文件977项通过、0失败/待定、346.73秒，全部本批进程终态。此前全量972通过/3失败的Skill管理异步等待用例单独82项复查通过，没有修改其代码或断言；重跑975项通过发生末版修正期间，不算最终同版证据。新增固定W0-W9结项索引，下一轮按原顺序逐项核定。Go/SDK/schema/依赖未改未重跑，未部署；全部能力、同版联合及最终review仍未完成。

G4.89当前更新：事件流在游标/批次读取时检查真实作品归属和当前成员，HTTP在开始、重放和保活时复查原会话及认证方式，撤权后断流；Project流为非纯delta事件增加通用快照通知。工作台补返修生命周期刷新、离线活跃3秒/空闲15秒轮询及撤权后的旧读取隔离。游标重置控制清空SSE ID，明确重置后允许权威低版本替换本地旧状态，同时保留草稿和未知请求；补修待确认消息缓存，避免再次刷新退回“正在发送”。纠正G4.88操作不可变事件表的测试夹具，生产触发器不变。新增五个Go顶层用例只有源码未执行，最后全后端build/vet通过；末版前端专项226项、全量66文件956项通过，0失败/待定、338.22秒，TypeScript、隔离Vite及四流程合同检查通过，全部本批进程终态。SDK/schema/依赖未改未重跑，未部署。该批修复收尾不等于全局review完成；原清单逐项闭环、同版联合及真实环境验收仍未完成，不能以单次回归时长估计整体工期。

G4.88当前更新：审批/对话返修创建、目标确认和显式重试在原事务中追加不可变执行授权，绑定实际工作区/用户、请求/定位/基线和指令hash。后台从该记录恢复主体，在领取及提案/无修改提交时重新检查当前权限；SDK提交沿持久执行身份取真实作者，不以服务身份、作品所有者或默认本地用户替代。旧请求缺失授权不自动推断作者，排队项记录失败后让出，用户可通过原执行/目标确认入口重新授权；旧幂等回执不重绑主体或重排队。失败清理保留授权错误码，正常等待不刷错误日志。前端区分授权不可用与模型失败，保留显式确认和只读限制。新增十个Runtime、一个真实Sidecar服务路径及一个HTTP状态映射顶层Go测试均只有源码未执行，未绕过Application Control。前端修复前七个授权展示反例失败，修复后专项28通过；末版全量66文件942通过，0失败/待定、263.02秒。全后端build/vet、TypeScript、隔离Vite及四流程合同检查通过，全部本批进程终态。SDK/schema/依赖未改未重跑，未部署。本项为源码修复、行为待验，不签署P1验收关闭或最终review完成。整体剩余小时数仍缺乏可靠依据，不另报未经核定的交期。

G4.87当前更新：审批卡可读取当前审批完整绑定集合并选择确切产物版本返修，覆盖托管单产物、逐集及不同类型多产物；旧剧本检查点不单独返修派生交接文件。纠正本批初版把集合版本号误当成员数量的校验。新请求持久入队，审批HTTP不再同步执行SDK，前端不因提交返修自动继续工作流；未知回执锁定原目标/指令，重试复用原键，临时只读恢复不丢选择。来源审批通过不可变创建事件投影到返修记录，阻止提前确认来源集合。对话候选不再漏掉有冻结输出合同的托管自定义产物类型，不直接放开任意未知类型。新增七个Go顶层用例及扩展既有HTTP检查点用例只有源码，未运行；末版全后端build/vet通过。前端扩大专项242项，随后同版全量66文件934项通过、0失败/待定、310.02秒；TypeScript、隔离Vite及四流程合同校验通过，全部本批进程终态。SDK/schema/依赖未改未重跑，未部署。沿入队追查确认P1代码缺口：revision Worker以无用户身份上下文执行，RevisionRequest及创建事件没有实际提交者绑定，现有权限复查不能保证覆盖后台领取/完成；下一项必须修复该共享授权边界，不能用作品所有者或默认用户代替提交者。原完整范围、真实验收和最终review仍未完成。

G4.86当前更新：将托管Skill单产物、逐集产物及多产物的人工保存、返修提案提交、接受修改接到同一冻结输出合同。沿当前版本的生成祖先读取ContextPack并校验hash/身份，不追随可变的当前attempt，不因最新生成合同缺失回退到更早合同；批量编辑绑定原episode_no，多产物用完整Schema加只读兄弟产物复验。BeginRevisionAttempt在事务内写入权威artifact_validation并重算上下文hash，Sidecar给SDK传递实际Schema；SDK最终补丁完成后校验，允许合法关联字段分步修改，no_change不伪造修改。提案/无修改完成回调复查当前授权、最新尝试与基线。另纠正G4.85漏接的多产物ContextPack领取门禁，以及单产物重生成后未更新artifact.step_run_id的问题。新增六个Go顶层行为测试及扩展SDK主体完成用例只有源码；末版全后端build/vet通过。SDK专项42、扩大专项146、最后全量1552通过，0失败/错误/跳过、369.66秒；前端返修/保存/审批专项52项和TypeScript通过，四原流程合同校验通过。全部本批进程终态，未部署。下一项是已确认的审批卡通用目标缺口：多产物和非script_unit批量返修仍只能用旧剧本/集数选择逻辑；须接确切产物版本选择和用户入口。原完整范围、Go行为/真实环境验收及最终review未完成。

G4.85当前更新：接入原本可声明的单任务、多类型one产物输出，SDK收到按artifact_type命名的完整对象Schema；Go同一事务校验并保存整组、依赖、主Task指针、全部版本回执与集合审批，重生成整组保存后才进入待确认。保持旧单输出及逐集batch协议，不将本批称为任意批处理。普通任务与singleton产物的审批查询已绑定真实step任务，编辑次要产物不再要求占用主输出指针；该例外只用于托管多产物，不放宽旧剧本交接规则。多产物编辑复验原冻结ContextPack和整组provider/输出Schema，旧提交回执按原attempt返回全部原版本，不变成后来的人工版本。SDK不再为托管直接产物默默丢弃未声明字段。六个Go顶层行为测试仅源码未执行；最后全后端build和全量vet通过。SDK专项205通过，随后全量1531通过、0失败/错误/跳过、292.15秒；前端集合确认专项30项和TypeScript通过，原四流程合同校验通过。全部本批进程终态，未部署。后续继续单产物托管Skill人工保存的格式校验缺口及Agent返修路径核对；原完整范围、同版真实验收和最终review仍未完成。

G4.84当前更新：托管Skill直接产物的可选provider响应Schema与目标产物Schema改为共同约束同一对象；ContextPack交给SDK联合Schema，Go提交同时检查两者，旧冻结任务仍检查原有两个合同。未提供或相同provider Schema保持原格式。修复状态化生成已采用JSON兼容模式、进入同合同输出修复却重置为structured的问题；暂停/重建保留兼容模式。扩大SDK专项193通过、0失败/错误/跳过、46.99秒，使用真实固定SDK Runner和模型/后端替身，不是真实网关或Go联合。新增三项Runtime及两项Capability顶层Go测试源码未执行；本批最后Go build、十包vet及原四流程合同检查通过。纠正无审批诊断：编译器原本已禁止model/batch直接confirmed，本批拒绝的是pending_approval与无审批等矛盾声明，不新增模型自动确认。不同类型多输出仍可声明但通用提交未实现，是明确代码缺口，不能通过拒绝声明计为完成。前端未改，未重跑完整分层套件；全部本批进程终态，未部署。原完整范围、同版回归及真实验收门禁未完成，无可靠整体剩余小时估计，不签署最终review。

G4.83当前更新：纠正G4.82末尾定位：托管Skill上传校验原本已拒绝全部batch，并非存在可成功安装但只在首步启动被拒绝的批量包。本批沿用既有episode_no逐项协议，接入model/batch声明、启动规划、包内直接输出Schema及批量版本集合审批，不增加Skill-ID分支或模型循环。支持串行/并行、每任务一个many产物和显式批量审批；其余批量协议仍明确拒绝，不宣称任意批处理均可用。补每个分集产物的资产/配置/决策依赖，目标集数服务端限制1至10000。新增两项Capability、六项Runtime顶层测试源码未运行；真实固定SDK Runner专项107项通过（模型/后端替身），前端确认专项29项及TypeScript通过，全后端build和十包vet通过，原四清单合同校验通过。测试包仅位于fixtures，未安装、未部署；全部本批进程终态。可选provider_result_schema_ref与直接产物格式、多输出/无审批声明仍须继续核对，原完整范围和真实验收门禁不变，不签署最终review。

G4.82当前更新：复核发现未注册执行器已有可用性门禁并随动态Skill刷新保留，不重复开发。修复Resume执行一个Runtime步骤后把后继当Worker的问题：现在逐步读取事务内Run状态，Runtime后继继续由Runtime执行，审批和真实完成即停，质量审核已经规划的任务不重复创建；同一命令再次遇到同名Runtime步骤时在新pending步骤让出，保留原回执，避免死循环。来源清单、剧本上下文、体量判断和剧本聚合传播真实推进结果；run.completed改在真正终点统一产生，不因中途聚合误报完成。新增五个Runtime顶层测试源码未执行；末版全后端build和十包vet通过，四原清单37步骤/21适配器合同校验通过，全部本批进程终态。前端/SDK/依赖/schema未改未重跑，未部署。下一项已确认入口缺口：startManagedWorkflowRun拒绝kind=batch首步，须连同规划、领取、提交和用户继续检查，不仅放开kind。原完整范围和真实验收门禁保留，不签署最终review。

G4.81当前更新：清单编译器和离线合同校验现在在注册前拒绝未知条件、同一条件指向不同目标及failure自环；原十种条件名称保持封闭集合，user_stop保留为已知但尚不可执行，并给出明确诊断，不改变用户停止彻底取消的行为。Go对象解析拒绝额外字段，仍兼容字符串completed简写及已编译快照的When/To字段。Node修复前六个非法条件反例失败，修复后最后完整回归40通过、0失败/跳过/待定、87.14秒；新增五个Go顶层用例仅源码，未运行，末版全后端build和九包vet通过。前端/SDK/依赖及数据库schema未改，未部署；manifest校验schema增加条件枚举。下一项只读核对动态Workflow清单与实际调度入口的一致性，尚不把待核查方向写成已确认缺陷。原四流程、三模式、W0至W9/GAP和真实验收门禁仍保留，完整review未完成，不能依据批次数或测试数给出完成比例和剩余小时承诺。

G4.80当前更新：状态化步骤最终失败现可选择清单声明的唯一failure后继；先等待自动重试和整批结算，原任务/尝试保持failed，后继独立pending、Run paused，经既有继续入口才创建执行任务。绑定失败项、确切输入和已完成工具参数/结果摘要，旧SDK checkpoint只保留引用、不作为新步骤恢复状态；未知写入或活跃/丢失PTY进程、保护失败、输入/写锁变化、独立重生成计划等不能借主流程跳转。恢复前重新校验封存决策和事实，非法分支记录阻止原因但不吞掉原失败；真实存储故障整体回滚。用户过程记录新增待继续/不可继续提示。新增七个Runtime顶层测试和扩展HTTP冲突测试均未执行；最后全后端build与九包vet通过，前端专项78、全量65文件900项通过，0失败/待定、268.80秒，TypeScript、隔离Vite及四流程合同校验通过。本批全部命令终态；未部署，SDK/schema/依赖未改未重跑。下一项为编译器只检查when非空、未知条件仍可注册的缺口；user_stop语义未获答复仍保持彻底取消。原四流程、三模式、W0至W9/GAP及真实验收门禁未完成，不签署最终review。

G4.79当前更新：user_continue已复用现有审批和决策快照接入普通推进、Runtime步骤及质量审核；只有确切版本/输入/目标的独立确认被用户批准，才创建后继。普通Resume不能绕过，取消不执行后继；新增通用流程确认的服务端投影，避免前端误显示扩写策略。全后端build和九包vet通过，四个Runtime加一个投影Go顶层用例只有源码未执行。前端专项33项通过；最后全量65文件898项通过、0失败/待定、232.73秒，TypeScript和四流程合同校验通过，所有本批命令终态。SDK/schema/依赖未改未重跑，未部署；user_stop/failure及原四流程、W0至W9/GAP和真实验收门禁继续保留，不签署最终review。

G4.78当前更新：补接incomplete_material_confirmed的封存缺集政策、当前部分成功审批及参考剧本聚合入口。删除按整个Run读取最新部分成功决策的旧逻辑，核对确切步骤、当前任务、原决策hash、审批事件及获批版本集合；补齐后不继承旧失败标记，聚合旧输入仍缺少新结果时拒绝。编辑待确认的部分结果会同事务创建新决策和新审批关联，保留缺集提示并要求重新确认，不能复用旧审批放行新版本。新增四个Go顶层用例只有源码，包含分支、故障状态及连续编辑后的审批接续；全后端build和八包vet通过，未运行受限Go测试。前端/SDK/schema/依赖未改未重跑，未部署。原user_continue/user_stop/failure三种条件仍未接推进事实，四流程完整交付、W0至W9/GAP及真实环境验收门禁保留；不签署最终review，不给未经核定的剩余小时承诺。

G4.77当前更新：复查四流程推进发现工作流一直取第一条后继，忽略when；内置体量分支同目标掩盖了问题。成功/审批推进现按Runtime事实选择后继，含普通审批、单选、改编、扩写、重生成回接和质量审核；批次全部成功读取当前真实任务集合。无匹配或不同后继同时匹配时返回409并回滚，不让模型选下一步。新增六个Go顶层测试仅源码未执行；全后端build、八包vet通过。四原清单37步骤/21适配器合同校验通过，原Node合同回归经正常子进程审批32通过、0失败/跳过、62.87秒；这些不验证新增Go分支行为。前端/SDK/schema/依赖未改未重跑，最近分层证据保持G4.76前端897和G4.68 SDK1507，全部本批进程终态，未部署。原合同的user_continue/user_stop/incomplete_material_confirmed/failure条件仍没有对应推进事实生产入口，已明确登记为代码缺口，不归因于外部阻塞。四流程完整交付、全部W0至W9/GAP及真实环境门禁保留，Goal仍active，不签署最终review。

G4.76当前更新：分集确认附带自动生成剩余集时，执行方式快照与确切审批版本现在进入同一ResolveApproval事务；前端删除先独立切模式的请求，避免确认失败而模式已改变。原空payload/null/空对象保持无附加设置语义，非法模式或错误审批范围拒绝；确认后的显式继续仍是独立命令，未声称整条确认加继续原子。独立模式命令复用当前授权和原回执目标检查。修复前两个App反例均失败；专项最后200通过，最后固定前端65文件897项通过、0失败/待定、272.69秒，TypeScript、隔离Vite、全后端build和七包vet通过。新增Go审批成功/回执恢复/故障回滚及控制授权行为用例均未运行，不将静态检查当作事务行为通过。SDK/schema/依赖未改未重跑，全部本批进程终态，未部署。继续四流程完整交付、SDK三模式联合及原W0至W9/GAP逐项收口；真实Go/OCI/网关/浏览器/回滚门禁未通过，仍无可核定的整体剩余小时承诺，不签署最终review。

G4.75当前更新：前端公共消息合同已收敛为AgentTurn，删除旧MessageExchange返回后补素材、配置和注入结果的同步编排；旧协议回执不清除原请求、不再触发后续写命令。修复直接恢复已完成回执时页面不读取最终结果，以及选择Skill发新消息前自动取消失败Run的问题；失败Run写锁仍由后端在用户确认启动替代任务的事务内释放。四流程覆盖接受回执、完成事件/断线轮询/已完成回执、服务端配置投影及显式配置/启动的真实App加mock API用例，不称为SDK/Go真实联合。首轮专项221通过4失败为控件名称断言问题；另有4项终态刷新和5项提前取消的修复前反例。首轮全量888通过1失败，定位为Skill管理离页警告用例未等待异步清理，改为等待实际警告解除，未修改产品保护。扩大专项316通过；最后固定前端65文件894项通过、0失败/待定、243.78秒，TypeScript及隔离Vite通过。Go/SDK/schema/依赖未改未重跑，原四流程后续完整交付、W0至W9/GAP及真实Go/OCI/网关/浏览器/回滚门禁仍保留；未部署，不签署最终review。

G4.74当前更新：消息接受入口新增只读原回执查询，事务内复核当前身份/权限、作品/对话及原键绑定；已接受请求在当前Skill/素材校验和新请求开关之前返回原执行的当前状态，不重新入队或唤醒执行器。新消息在自动补素材前固定带版本标记的原请求哈希，复用create_message幂等表；旧哈希只按原协议核对，自动素材只从不可变接受回执重建，不取当前素材或排队编辑后的正文。前端恢复旧消息不再先取消当前失败Run，收到错误归属/状态/格式回执保留原请求，停止后的迟到成功不覆盖新提交，恢复时4xx不声称原操作未执行。专项首轮185通过24失败，修正测试回执缺少submission_id和跨用例残留后209全部通过；最后固定前端全量65文件877项通过、0失败/待定、218.78秒，TypeScript、隔离Vite、Go build及七包vet通过。新增Go行为用例未运行，SDK/schema/依赖未变，未部署。下一项继续原W5/W9及四流程契约：当前公共消息API仅返回AgentTurn，前端仍保留同步MessageExchange返回后的流程补接，须核对并收敛，不以旧协议夹具替代SDK完整链路。原完整范围和真实环境门禁保留，不签署最终review。

G4.73当前更新：四流程首条消息及工作台普通消息已复用按工作区/用户/作品/对话隔离的sessionStorage原请求记录，保留原键、客户端ID、精确Skill版本、附件/封存批次和提交上下文；先标记pending再发送，重建只恢复未知请求，不自动重发或静默换Skill版本。补停止/迟到响应隔离、显式按新请求编辑、只读角色门禁、存储故障锁写及已确认后纯本地清理，旧无归属记录不展示/迁移/删除。专项4文件103项及实际App夹具扩大2文件152项通过；最后固定前端全量65文件860项通过、0失败/待定、214.98秒，TypeScript和隔离Vite通过。上述是组件重建/实际App加mock API证据，不是实际浏览器重载或真实后端验收。复查确认后端createMessage在查原回执前校验当前Skill/素材/选区，且自动补素材后才计算请求哈希，旧请求可能因环境变化无法恢复；该代码缺口尚未修复，是下一项明确工作，不归因于外部阻塞。Go/SDK/schema本批未改未重跑，原四流程、W0至W9/GAP及真实环境门禁保留，未部署，不签署最终review。

G4.72当前更新：Skill管理八类写命令已增加按工作区/用户隔离的IndexedDB原请求记录，ZIP保留原二进制、文件名、MIME及时间戳；先提交本地记录再发请求，页面重建后只恢复原命令而不自动发送。并发页面不能覆盖待确认记录，旧清理不能删掉新命令；存储异常锁写但目录仍可读。修复显式刷新不能解除另页已完成记录的旧锁、角色降级误拦纯本地清理，以及sessionStorage写回失败丢掉已读取客户端ID的问题。专项4文件106项通过，最后同版前端全量64文件831项通过、0失败/待定、250.66秒，TypeScript及隔离Vite通过。fake-indexeddb/组件重挂载不是实际浏览器重载或磁盘持久化验收；Go/SDK/schema未改未重跑，Go行为禁令和真实环境门禁保留。下一项明确为四流程共用的首条消息交接：App在请求发出前删除sessionStorage记录，剩余原键/材料仅在内存，不能称整页刷新后仍可恢复；还须核对精确Skill版本和身份绑定。原四流程、W0至W9/GAP完整范围不缩小，未部署，不签署最终review。

G4.71当前更新：ZIP安装/升级、目录纳管/更新已接公共持久命令协议和前端原请求恢复，复用原隔离校验、安装事务、不可变版本、安装事件及幂等表。升级在事务内核对用户观察的完整版本/状态/启停/事件数，新安装不再覆盖已安装同名包；原键取回原审计回执，不重放后来的升级/卸载/重装，也不依赖原目录仍存在。前端核验ZIP原字节哈希、确切范围/版本/事件，未知结果保留原File/目标/键，支持只读刷新和同页重试；中文文件名改用RFC5987。修复已提交安装在读取失败后被标为失败，以及状态更新零行仍继续提交的问题。最后前端全量62文件790项通过、0失败/待定、221.51秒，TypeScript、隔离Vite、Go build及七包vet通过；新增Go行为源码未运行。SDK及schema未改、未重跑SDK，未部署。整页重载的原请求恢复仍未实现；原四流程、W0至W9/GAP及真实联合验收不缩小。

G4.70当前更新：继续原Skill管理生命周期，启停/版本切换/卸载现已在源码中接用户观察快照与持久幂等回执；复用现有安装事件及idempotency_records，不新增schema。事务内复查身份/范围、当前版本/状态/启停/事件数，读取期间快照变化也拒绝；原请求重试先核对权限和原审计回执，再返回当前安装状态，不重放卸载前、重新安装前或后续版本前的旧操作。前端传原快照和键，校验回执/包版本/事件归属，结果未知时锁竞争操作、允许只读刷新和原请求恢复；撤权不冒充原操作未执行。新增Go行为源码未运行。最后前端全量61文件768项通过、0失败/待定、204.62秒；TypeScript、隔离Vite、Go build及七包vet通过。本批进程终态，SDK/依赖未改未重跑，未部署。ZIP安装/升级和目录纳管/更新仍缺同等持久命令保护，浏览器整页重载的原请求恢复也未实现；原四流程、W0至W9/GAP及真实环境验收不缩小。

G4.69当前更新：用户再次询问review工期，未给未经核定的小时承诺，继续原Goal。修复旧Skill管理的版本选择自动重新启用已禁用安装的问题，受支持版本现在保留enabled并同步固定注册版本；新增Runtime及HTTP行为源码，因系统限制未运行。管理页补同步提交锁、身份/角色变更和卸载页面后的迟到结果隔离、刷新失败后的写入口失效与只读重读、目录预览目标/版本校验，以及ZIP选择窗口的目标冻结。前端全量61文件750项通过、0失败/待定、212.55秒，TypeScript、隔离Vite、Go build及七包vet通过；SDK未改未重跑，schema未变。旧ZIP安装/升级、回滚、启停、卸载仍缺完整用户快照CAS及持久幂等回执，本批页面保护不能替代该后端缺口，也不声称未知操作可跨页面原键恢复。原四流程、W0至W9/GAP及真实环境验收仍未完成。

G4.68当前更新：核对对话作者工具、草稿校验/审批安装、不可变版本及当前执行目录接线，修复工作文件窗口刷新/关闭导致原安装状态丢失、错误回执误报成功、工作台吞掉目录刷新失败以及升级后仍发送过时Skill选择的问题。安装/升级绑定确切作品、范围、安装和包版本，未知结果只能重试原操作；已保存但非当前启用的版本不再声称可调用。完整App验证安装后目录出现新Skill并发送准确版本，含刷新失败后只重读、不重新安装。前端全量61文件738项通过、0失败/待定、197.97秒，TypeScript和隔离Vite通过，Go build和七包vet通过，Go行为未运行。固定SDK全量经正常依赖读取审批后1507项通过、0失败/错误/跳过、309.04秒，SDK及OpenAI版本未变，全部本批进程已终态。Skill管理页ZIP升级/版本回滚/启停/卸载的旧提交边界是下一项具体复查；原四流程、完整W0至W9/GAP及三模式真实验收不缩小。

G4.67当前更新：继续原附件完整生命周期检查。视频批次已接前端移除、排除/纳入和重新收集入口；移除ZIP子视频会解除整包引用并保留其余独立附件，切换Skill时删除材料不再复用旧批次。视频提交只使用已纳入成员；通用对话的混合附件保留其他文档，不自动把它当作纯视频批次。多文件上传逐项保留成功结果，新建作品可移除已上传附件；原操作未知时禁用竞争操作，关闭弹窗后仍可重开重试。后端集合/版本读取补确切配对，运行输入拒绝错配。专项5文件81项通过、19.94秒，TypeScript和隔离Vite通过，Go build和七包vet通过；Go新增行为用例未运行。最后同版前端全量61文件703项通过、0失败/待定、209.29秒，已重读JSON核对，所有本批进程已终态。原四流程、Skill闭环、W0至W9/GAP及真实环境门禁仍需完成，无可靠剩余小时估算。

G4.66当前更新：同一Run的每个聚合版本改为独立候选，原最终稿不再由新聚合更新；完成后编辑只让修改正文及刷新交接进入待确认，未改动旧正文保持确认状态。schema64迁移源码保留候选ID及定稿/预览/导出引用，没有运行用户数据库迁移，Go行为待验。新完整工作台测试发现看过新版后切回旧稿仍显示新版，已修复显式候选导航与普通刷新版本门禁的冲突。候选/工作台专项2文件139项通过、43.75秒；最后前端同版全量60文件674项通过、0失败/待定、198.40秒。TypeScript、隔离Vite、全后端build及七包vet通过。还须完成原四流程、附件增删/运行输入一致性、Skill闭环及W0至W9/GAP复查，不将本批版本保留修复等同整个任务。没有可信的剩余小时估算，不复用此前交期。

用户指出从周六开发到周二仍未交付。此前重复给出的“20–35 小时”没有可靠的剩余工作估算支撑，撤回作为当前交期；下方保留原记录供追溯，不继续引用它或换一个未经验证的时间承诺。主要问题是最初能力盘点和工作量低估、底层增量持续推进但用户完整任务迟迟未贯通。外部验收条件确有阻塞，但不能解释或掩盖尚未完成的代码。

G4.42 已接通原生交互工作区的三个执行入口，G4.43 进入其用户任务联合回归；G4.44 补真实父执行入口的子任务生命周期矩阵，并继续跨模块代码 review，修复三类授权边界问题。G4.45 继续执行恢复、启动关闭及迁移审查，修复流式断连清理、Worker 部分启动和迁移完成标记问题，含新增用例的 SDK 全量1507通过。不是“尚未写这些代码”，也不是“只剩最终 review 签字”。以下按原 GAP 和交付门禁归组，不新增产品方向；每批更新同一组的实现、接线和验证状态，不把新增测试数量当成完成比例。

| 交付组 | 尚需完成 | 完成证据 |
| --- | --- | --- |
| D1 原生工作区适配，GAP-08 | G4.35 已补 SDK Filesystem/Shell/Skills 装配：绑定会话后生成的工具接原审批/审计/过滤，Skill 元数据来自实际装载目录。SDK Runner + 真实临时文件验证发现、补丁审批后重建、单次 Shell、结构化图片及关闭失败交付检查。当前路径不需安装或运行额外启动助手，未放宽 noexec；PTY和生产源码接线进展见D2/D3，真实Go/OCI联合仍未通过。 | 真实 SDK 会话创建、文件读写、精确持久化与重建恢复；不是仅测试传输替身。 |
| D2 执行生命周期，GAP-08 | G4.42 将三个生产源码入口切至 SDK 公开外部 session 和 PTY provider；独立保存版本化工作区恢复引用，从 Runtime 权威目录重绑定原环境/进程，旧 SDK 文件 checkpoint 经严格验证后迁移。真实 SDK + 文件/引擎夹具验证跨两次审批重建、批准/拒绝、环境丢失不重启及 stdin 不重放。配置仍默认关闭，未部署；Go仅build/vet，真实Go/OCI联合待验证。 | 受限引擎中执行、交互、取消、重建和清理，审批与未知结果不重放。 |
| D3 用户工作区闭环，GAP-01/02/08/09/14 | 多文件/二进制发布下载、工作文件窗口、Skill 校验安装和依赖激活恢复已有接线。G4.43 新增三模式 SDK 多文件编写→发布→校验→安装→加载资源联合验证，以及原生暂停/模型故障→追加→恢复、状态化无工具输出修复，11项通过。仍待真实 Go 发布/注册/迁移/跨进程联合、用户页面与真实环境验收，不能将注册表替身算作真实安装验收。 | 用户从页面发起多文件 Skill 编写、确认安装、直接调用、修改升级及成果下载，不需要开发者手动注册；覆盖三种执行模式。 |
| V1 原范围同版回归，GAP-01 至 GAP-17 | 最新SDK为G4.92固定源码全量1610通过、0失败/错误/跳过、256.99秒，8文件专项179通过。前端最近全量仍为G4.90的66文件977项及TypeScript/隔离Vite，本批前端未改未重跑。G4.89新增Go行为及G4.88夹具修正未执行。不拼接历史层级结果为同版全部通过，不以模型/服务替身替代真实网关/Go联合；失败与权限阻塞原记录保留。 | 同一代码版本的 Go、SDK、前端全量及三模式联合矩阵；分层夹具、真实模型、浏览器证据分别标明。 |
| V2 真实环境验收，含 GAP-04/05/07 | 在获准且兼容的隔离环境完成真实 OCI、网关工具/原生压缩、浏览器用户闭环和回滚验证。代码可实现缺口与外部阻塞分开处理。 | 原会话压缩/继续、真实文件和工具结果、用户操作及版本一致性证据；外部条件未满足仍为未通过。 |
| R1 全局 review 与修复 | 进行中。G4.91修评测校验，G4.92核对W1并修名称路由/隐式策略，G4.93修同名不同ID脚本快照和调用参数；Go只有静态证据，行为待验。G4.88/89后端授权P1仍为源码/静态证据，行为待验。下一项W2供应链，按固定W0-W9继续，不因外部阻塞重复静态重查；原四流程、三模式、user_stop及真实验收边界不变，不是只剩最终报告。 | 所有必交项状态可追溯，未解决缺陷/未验收条件明示；未达标不完成 Goal。 |

执行依赖：D1/D2 的基本保存/续租/清理、D3 发布与G4.42的外部session所有权和独立恢复引用均已进入生产源码，不再列为待开发。固定SDK提供外部session时不序列化sandbox RunState，平台只保存有界的会话/环境/快照引用并核对Runtime登记，不复制模型循环或把文件快照冒充活进程。G4.43验证原生追加/修复和多文件Skill作者矩阵，G4.44继续父子任务联合和跨模块审查；按V1/V2清单核定剩余联合验收，最终R1结论仍以完整范围和受影响回归为前提。针对性自查不冒充最终review；真正新增产品需求必须另行说明，不静默扩成复制整个Codex App。

范围待明确项仍保留在原能力审计：语音/实时、Computer、PTC 等不冒充已实现，也不未经确认展开新产品线。G4.48已向用户发出异步范围确认，未收到答复前不视为排除或承诺完成，继续已明确范围的工作；这不是部署、外部账户或系统策略授权。Hosted MCP/WebSocket/不同存储等替代实现不能重复计为必建功能。原生 CustomTool 补丁已有适配，其真实网关兼容性纳入上述验收。

外部条件当前为：Go 测试程序被 Application Control 拒绝；实际 OCI 不可用且未获安装授权；Responses/compact 网关兼容未通过；浏览器验收权限待明确。禁止通过更名、换路径或修改策略绕过，不操作测试机、用户作品/数据库或 8860/8880。

最近证据分开记录：G4.42扩大专项527项通过110.62秒；旧checkpoint兼容及实际入口17项通过20.93秒；随后SDK全量1482项通过384.37秒。全量原进程曾中断在14%且无XML，经只读确认退出后保留原日志重启，完整证据`.tmp/goal-g442-sdk-all-resumed.xml`/log。G4.43包含新增11项的SDK全量1493通过333.48秒，`.tmp/goal-g443-sdk-all.xml`/log；前端同版350项通过，`.tmp/goal-g443-ui-all.json`，TypeScript和隔离Vite构建均退出0，见`goal-g443-ui-types.log`/`goal-g443-ui-build.log`，产物仅在`.tmp/goal-g443-ui-dist`。G4.44在新增六项子任务联合后SDK全量1499通过373.64秒，`.tmp/goal-g444-sdk-all.xml`/log；本批修改后端授权源码，最后全后端build和六包vet通过，`.tmp/goal-g444-review-build.log`/`goal-g444-review-vet.log`。没有运行被限制的Go测试或真实PTY；完整Go行为回归仍来自G4.21，不能覆盖后续源码。未部署，不能声称用户当前页面已具备最新能力。

## 批次记录

### G4.93 同名Skill脚本身份与审批一致性（2026-09-10）

- `_selected_skill_scripts`现在以capability ID保存独立快照及允许的脚本ID；显示名称只是验证字段。`execute_skill_script`新增默认空的ID参数，显式ID必须对应当前选中包的名称/脚本，旧调用只有组合唯一时才接受，歧义/未知/未选中均拒绝。登记后冻结原参数，不在执行时按动态目录重选，原省略ID与显式空ID都按原JSON字段存在性提交，避免改变审批哈希。
- Go strict JSON解码接受可选ID；新登记与invocation/选中快照一致，已有调用重试与原固定快照一致，执行前再次核对；无单独快照的旧入口也按名称/脚本唯一性选取，不因Registry列表顺序任选。未改schema或既有审批哈希算法，不重写旧记录。旧checkpoint的无ID歧义不猜测原包，须重新显式发起。
- `.tmp/goal-g493-identity-before.log/xml`：11失败/2通过、7.46秒；初修身份和既有runtime用例21通过、4.87秒：`goal-g493-identity-after.log/xml`。扩大9文件218通过、146.69秒：`goal-g493-skill-regression.log/xml`。末版新增三执行身份、同名共享脚本审批恢复矩阵，完整SDK回归进行中。固定SDK Runner真实运行，模型/后端/沙箱为替身，本地stdio/HTTP是隔离夹具，不是网关/Go/OCI/用户浏览器验收。
- Go新增一项顶层八个子场景：跨workspace/project同名安装、显式选中、唯一旧调用、歧义、错误ID/脚本及快照冲突、原审批复用。仅源码，未运行Go测试。全后端build和vet退出0：`.tmp/goal-g493-build.log`/`goal-g493-vet.log`。未绕过Application Control，未使用浏览器/子任务，未操作8860/8880、测试机、用户作品、数据库、网关或凭据，未push/部署。前端与依赖未改。
- W1/W4具体源码问题修复后继续W2安装供应链，不将本批代码和分层夹具计为整组或原Goal完成；真实Go/OCI/网关/浏览器验收和最终review继续保留。

### G4.92 W1动态调用策略核对（2026-09-10）

- 当前读取标准包解析、YAML可选元数据、四级选择、动态Registry、目录纳管/卸载覆盖、冻结版本和作者安装调用源码；Go测试源码覆盖范围和版本，但本批未运行Go。固定依赖实读`openai-agents=0.21.1`、`openai=3.3.1`，未升级。固定结项清单增加逐条W1证据，而不是复制早期completed标签。
- 自然语言中的Skill名称现在只收集候选；SDK判断是否真要使用，考虑拒绝/引用/解释和显式调用策略。仅名称命中不再自动返回选中ID；UI确切引用保持原直达分支。目录补路径和调用策略，先检查完整目录中的本轮提及，再应用显示预算，不加载全部指令。共享`allows_implicit_skill_invocation`让默认允许、任一显式禁止均一致生效；未选中发现不再加载禁止隐式调用的包。保持明确选择、批准安装后当前轮加载和审批恢复。
- 首批反例9失败/1通过：`.tmp/goal-g492-policy-before.log/xml`；初修10通过。扩大专项30通过/2失败是新增模型夹具只记录位置参数，固定SDK实际用关键字参数；修正夹具记录，不改生产接口或放宽断言。首次8文件回归143通过/1失败/35错误，35项被pytest临时目录权限挡在setup，本地stdio兼容项报告blocked；原日志保留。经正常审批原样重跑8文件，179通过、0失败/错误、65.93秒：`.tmp/goal-g492-skill-regression-authorized.log/xml`。
- 测试包含真实固定SDK Runner接受结构化路由决定、动态依赖、三执行身份作者发布/安装/加载与审批恢复、渐进资源读取及本地stdio MCP；模型、后端和原生沙箱使用替身，不是模型语义质量、Go事务、OCI或用户页面验收。最后完整SDK在固定源码上1610通过、0失败/错误/跳过、256.99秒，`.tmp/goal-g492-sdk-all.log/xml`，已解析XML复核。全部本批命令终态，受影响源码diff检查通过；无改动的Go/前端不重复构建。
- 后续具体缺陷：直接调用`_selected_skill_scripts`，两个capability ID为scope_a/scope_b、name同为same-name且都有review脚本，允许集只有(same-name,review)，快照只剩scope_b。当前`ensure_call`按name取该快照，不能区分用户意图中的两个Skill；Go确切快照校验不能恢复此前已丢失的选择。源码位于agent_tools.py的_selected_skill_scripts/ensure_call和runtime.py的execute_skill_script，下一项继续此W1/W4共有问题，不当作外部阻塞。
- 无push/部署/测试机/8860/8880/用户作品或数据库操作，无Go测试、浏览器、真实网关或OCI执行。原完整范围和最终review继续保留，Goal active。

### G4.91 W0基线及路由评测校验（2026-09-10）

- 核对`platform-contract-golden.json`及其Go比较测试、`.agents/skills/outline-critic`和`fixtures/skills/stateful/story-review-workflow`。夹具原本存在，不新装Skill，不改Golden；Go比较和实际推进测试未运行。
- 合同反例首次Node子进程`spawn EPERM`；经常规审批运行相同Node测试，40通过、0失败/跳过、83.64秒：`.tmp/goal-g491-contract-tests-authorized.log`。未借此执行受限Go二进制。`.tmp/goal-g491-stage8-plan.log`的99/99为计划分配校验，不是业务测试通过。
- `evals.py`校验目录条目、严格字段类型、唯一ID/版本组合和模式/creates_run关系；引用必须匹配版本，期望/禁止能力必须存在且不矛盾。四原流程和三种新增Skill模式必须有正向路由断言，负例或重新贴标签不能代替；不同合法固定版本、带换行提示和带明确引用的负例继续支持。不升级SDK或修改合成路由夹具内容。
- 修复前20失败/3通过：`.tmp/goal-g491-evals-before.log/xml`，测试主体确实执行，另有pytest缓存权限警告；后续关闭非必需缓存插件，无环境策略变更。初修23通过，末版30通过、0失败、0.37秒：`.tmp/goal-g491-evals-final.log/xml`。本模块消费者检索只有测试引用，不冒充真实模型路由或安装验收；本次未重跑无改动的Go/前端全量。
- 发布矩阵和证据脚本仍强制真实网关、完整E2E及OCI门禁；不将静态计划当发布回执。全部本批命令终态，源码diff检查通过，无push/部署/用户环境操作。W0未签收项保留，下一项W1，Goal active。

### G4.90 当前身份刷新与首页异步操作隔离（2026-09-10）

- `App`原先只在挂载时读取`auth/me`；HTTP中间件已经每请求`ResolvePrincipal`，因此本轮定位为前端旧权限/旧身份延续，不将其误报为后端未授权写入已被复现。现在焦点、路由、重新可见及可见页面30秒轮询更新身份，合并普通并发读取；SSE撤权强制重新核验并使更早读取失效。旧登录/读取结果不能覆盖新会话，卸载移除监听和定时器。
- 明确认证/成员权限失败返回登录页；临时网络或5xx保留当前视图，服务端仍是权限权威。Cookie认证不能自动退回隐式本地身份；登录成功响应可能仍描述bearer认证，因此从成功创建会话时另记Cookie预期，不等首次`auth/me`后才启用保护。同身份角色变化保留当前输入，跨账号/工作区使用既有根key隔离；未确认的原发送记录不删除或自动重放。未承诺所有未提交表单跨退出登录恢复。
- 首页新增/重命名/删除入口服从当前角色；同主体变更角色仍保留首页Prompt。创建、批量附件上传、视频批次接续与回执导航绑定发起时的用户/工作区/角色代次，变更或卸载后停止后续动作，不声称撤销此前已被服务器接受的操作。重命名版本重读/冲突重试同样检查原代次；作品读取与删除回执不覆盖新身份。
- 初始身份专项11项失败：`.tmp/goal-g490-ui-auth-before.json`；修复后11通过。扩大前置复查12通过/3失败，其中空作品创建夹具按钮名称不匹配，已对齐现有“创建并打开”，没有为测试改产品标签。首页和晚创建反例修复后扩大三文件207项通过：`.tmp/goal-g490-ui-auth-expanded.json`；之后追加重命名代次用例，专项20通过。
- 新登录Cookie预期反例先1通过/1失败：`.tmp/goal-g490-ui-login-before.json`；修复后末版专项22项通过：`.tmp/goal-g490-ui-auth-complete-focused.json`，其余180项为名称筛选未运行，不计作失败或全量完成。
- 全量首轮972通过/3失败：`.tmp/goal-g490-ui-all.json`，均为独立SkillManagement组件异步查找/等待失败；本批未改该组件或测试。单独复查82项通过：`.tmp/goal-g490-ui-skills-recheck.json`。第二次全量975通过、0失败/待定、344.63秒：`.tmp/goal-g490-ui-all-final.json`，但运行期间追加Cookie修正，**不作为末版同版证据**。最后固定源码全量66文件977项通过，0失败/待定、346.73秒：`.tmp/goal-g490-ui-complete.json`及log。所有本批测试/构建进程终态，未根据观察超时重启测试。
- 末版TypeScript与隔离Vite退出0：`.tmp/goal-g490-ui-types-complete.log`、`.tmp/goal-g490-ui-build-complete.log`；构建只在`.tmp/goal-g490-ui-complete-dist`，保留585.49kB包体积警告。四原清单37步骤/21适配器合同校验通过：`.tmp/goal-g490-contracts.log`。未运行Go行为、真实浏览器、模型/网关或OCI，未改数据库/用户作品/部署/8860/8880/测试机/系统策略。
- 已新增固定原范围结项清单并链接回基线。各组尚未逐项签收，不因这份索引或本批测试通过宣称全功能完成；后续从W0开始，只对已确认缺陷修复复测，不为外部阻塞反复重做已完成的静态核对。Go/SDK及固定依赖本批未改未重跑。

### G4.89 事件流持续授权、通用刷新与重置恢复（2026-09-10）

- `event.go`在同一只读事务中核对作品/Run归属、当前工作区成员和事件游标/批次；`event_stream.go`在连接、重放及保活时复查原身份和认证方式。退出、过期或成员撤销不能静默转为隐式本地身份继续收流；发不含私有正文的撤权控制后关闭。已授权读出的批次不声称能事后撤回。
- Project流每批非纯delta事件增加`stream.snapshot_invalidated`控制，保留原业务事件和持久ID，不另写数据库事件。工作台校验作品/正整数序号后合并只读刷新；补返修queued/waiting_safe_checkpoint/no_change/stale兼容监听，离线活跃任务3秒、空闲作品15秒读取。撤权后关闭源、停止轮询并使旧读取失效，初始加载中撤权也可退出加载态。
- `stream.reset_required`按SSE协议清空Last-Event-ID。明确重置后的快照可替换旧本地高版本，普通刷新版本保护不变；失败读取保留重置标记，草稿保留，未知消息不自动重发。随后复核复现连续刷新把未知消息重新显示为sending，修复时同步更新待确认缓存。此行为不是通用在途写命令epoch隔离，也不替代真实浏览器回滚验收。独立面板仍依各自数据入口刷新。
- 新增两个Runtime和三个HTTP顶层Go行为用例，包含跨批次撤权、会话失效、通知帧及拒绝断流；既有重置协议用例要求空ID。G4.88缺授权旧请求改用临时库中的历史形态请求行，损坏最新授权改用追加新事件模拟，不再UPDATE/DELETE不可变事件，不改生产触发器。**上述Go行为均未执行。**
- 保留初轮前端223项中222通过/1失败的记录，修正重连用例React effect等待后专项223通过。重置修复后全量955通过，但不将其算作最后缓存修复后的同版结果。缓存反例先失败：`.tmp/goal-g489-ui-message-before.json`；末版专项226通过：`.tmp/goal-g489-ui-complete-focused.json`，65.49秒；最后固定源码全量66文件956通过，0失败/待定、338.22秒：`.tmp/goal-g489-ui-complete.json`及log。
- 最后Go build/vet退出0：`.tmp/goal-g489-build-reset.log`、`.tmp/goal-g489-vet-reset.log`；初轮vet发现新测试SchemaVersion类型错误，已修正。末版TypeScript及隔离Vite退出0：`.tmp/goal-g489-ui-types-complete.log`、`.tmp/goal-g489-ui-build-complete.log`，产物仅在`.tmp/goal-g489-ui-complete-dist`，保留582.31kB主包警告。四原清单37步骤/21适配器合同检查通过：`.tmp/goal-g489-contracts.log`。全部本批命令终态。
- SDK/schema/依赖未变未重跑，未push/部署/操作测试机或8860/8880，未运行受限Go测试、浏览器、真实网关或OCI，未修改用户数据或系统策略。后端P1仍待行为验收；原完整清单及同版联合/真实环境门禁不因本批通过而关闭，Goal保持active。

### G4.88 返修后台提交者持久授权（2026-09-10）

- `revision_identity.go`将执行授权保存于既有事件表，记录真实工作区/用户、对话/消息/定位、产物/基线、指令hash及授权来源/版本。审批与对话共用创建事务；候选确认和显式执行重授权与原状态/幂等回执同事务。最新记录缺失或不匹配时拒绝，不回退到旧记录或作品所有者。
- `SidecarService.Execute`恢复确切授权用户，Runtime在领取和提案/no_change提交事务内复查。已有Agent活动保留原执行绑定。只有内部无身份维护可将仍无有效授权且版本未变的排队项标为失败；健康授权、更新后授权或已运行项不被旧维护覆盖，存储错误不冒充权限失效。正常安全检查点/队列等待不刷错误日志。
- 旧请求需用户显式重授权，不自动填作者；回执重试只读取原请求当前状态，不替换后来授权或重放模型调用。运行后撤权仍允许失败清理，但不能提交提案。没有执行用户数据库迁移、修订用户数据或切换用户服务。
- 正常测试夹具显式传入测试用户，保留无用户上下文的真实Worker边界。新增十个Runtime顶层用例涵盖存储重开、非所有者提交、撤权、SDK持久作者、审批作者、目标确认、旧请求、回执、损坏最新授权、回滚与队列维护；Sidecar服务路径用例覆盖模型调用前及响应时撤权，另有HTTP状态映射用例。**以上Go行为源码未运行，build/vet不等于行为通过。**
- 前端新增授权失败说明及显式执行确认，普通模型失败、原回执和只读限制保持。修复前七个反例失败：`.tmp/goal-g488-ui-before.json`；修复后专项28项通过：`.tmp/goal-g488-ui-focused.json`。末版全量66文件942项通过、0失败/待定、263.02秒：`.tmp/goal-g488-ui-all.json`及log。
- 全后端build退出0：`.tmp/goal-g488-build.log`；最终全量vet退出0：`.tmp/goal-g488-vet-complete.log`。TypeScript和隔离Vite退出0：`.tmp/goal-g488-ui-types.log`、`.tmp/goal-g488-ui-build.log`，构建仅在`.tmp/goal-g488-ui-dist`，保留580.94kB主包体积警告。原四清单37步骤/21适配器合同通过：`.tmp/goal-g488-contracts.log`。`git diff --check`通过；全部本批命令终态。
- SDK、数据库schema及依赖未改未重跑。无push/部署/测试机/8860/8880/浏览器/真实模型或OCI操作，无策略变更。P1保留行为待验，原完整能力核对、同版联合回归及真实环境门禁仍未完成，不标记Goal完成，不据本批测试数量估交期。

### G4.87 通用审批返修目标与后台执行边界（2026-09-10）

- 上一轮主要回答工期，按no progress处理；原测试句柄84444、12000、96146已在上一轮核定退出0，前端初版专项67通过。本轮重新检查当前代码后继续原目标，保留完整范围和全部环境边界。
- 新GET `/api/v1/approvals/{id}/revision-targets`复用现有授权，返回确切审批快照及六字段目标摘要，不返回产物正文。POST原`regeneration-requests`仅`request_ai_revision`允许可选`target_artifact_version_id`；未提供时字段不进入序列化哈希，保留原单产物/集号请求回执。所选版本须属于当前完整审批集合，过期或缺失兄弟产物、归属/范围/顺序/hash改变不得悄悄缩成可用子集；只在固定剧本组合执行器排除派生handoff。
- 读取实际审批构造器后纠正初版`subject_version == 成员数`的错误假设；该字段是集合版本号，完整集合以有序版本列表hash核验。审批返修仍只创建局部修改请求，不创建重生成计划、不写产物、不自动批准。来源审批ID从不可变`revision_request.created`事件派生，供列表/快照阻止提前确认，不新增数据库schema。
- 审批HTTP删除请求内同步SDK调用，沿现有Worker队列执行；前端提交返修后不走确认加Resume路径。原生select明确选择目标，读取失败可独立重读，未知提交结果冻结指令/目标、复用原请求键；同版本显示刷新和临时只读恢复保留选择，替换审批丢弃旧读结果。校验错误归属/基线/状态/版本回执。此批不是跨整页重载持久请求日志，不声称浏览器重载E2E已经完成。
- 对话候选固定类型表会遗漏托管自定义多产物，现未知类型必须具有本运行生成基线的真实冻结输出合同才可成为候选。先关闭产物读取游标再查询冻结合同，版本JOIN同时绑定artifact_id；保留旧派生交接不可语义返修的行为，不增补Skill-ID分支。
- 新增七个Runtime顶层用例源码，覆盖单/批量/多产物/旧检查点目标、集合版本不是成员数、入队与来源字段持久化、原键/过期回执/错误目标/竞争请求、完整集合变化、撤权、事件写故障回滚及旧集号兼容；另扩展原HTTP检查点用例验证GET、显式与省略字段、回执、取消和零同步执行器调用。旧剧本检查点使用明确状态夹具，托管测试使用临时目录安装与模型结果夹具；全部Go行为未执行，不把新增用例源码或vet当通过。
- 前端扩大5文件242通过、0失败/待定，`.tmp/goal-g487-ui-expanded.json`；随后末版全量66文件934通过、0失败/待定、310.02秒，`.tmp/goal-g487-ui-all.json`/log，进程79146退出0且已重读统计。TypeScript退出0，`.tmp/goal-g487-ui-types-expanded.log`；隔离Vite退出0，`.tmp/goal-g487-ui-build.log`，输出仅`.tmp/goal-g487-ui-dist`，主chunk580.62kB提示保留。最终全后端build/vet退出0，`.tmp/goal-g487-build-complete.log`、`.tmp/goal-g487-vet-complete.log`；合同校验4/37/21通过，`.tmp/goal-g487-contracts.log`。限定diff和新文件尾随空白检查通过。SDK本批未改未重跑，不合并旧SDK结果为本批真实联合；所有本批测试/构建进程已终态。
- 追查真实启动源码确认下一P1：`main.go`以普通shutdownContext启动revision Worker；`poll`和`Execute`没有恢复请求作者，`revision_requests`与创建事件也没有保存提交者身份；`authorizeRevisionAttemptTx`无用户/活动上下文时继续到仅对存在用户执行授权的文件门禁。此前G4.86的“完成回调当前授权”只证明带身份调用路径，不覆盖实际后台Worker。须持久绑定真实提交者并在领取/完成时重新核定当前角色/成员状态，处理旧无归属请求与队列失败，不默认冒用作品所有者、本地用户或系统权限。本项仍是未修复代码，不归入外部验收阻塞；不得将本批前端通过称完整返修交付。
- 未push/部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI、变更依赖或系统策略、启动浏览器或子任务。Go执行禁令及真实环境门禁不变，原四流程、三模式、W0至W9/GAP完整范围继续，Goal保持active，不签署最终review或新的小时承诺。

### G4.86 冻结产物编辑合同与 Agent 返修（2026-09-10）

- 上一状态问答只核对记录，按no-progress处理；本批实际修改生产源码、补测试并取得结果，不把状态汇报算开发。
- `artifact_edit_contract.go`复用生成合同，覆盖托管单产物、既有episode_no批量及不同类型多产物。当前版本沿base_version_id追溯最近模型生成版本后再查ContextPack；缺失/错误的最新合同必须拒绝，不回退。Schema来自原任务，不从当前可编辑包重新加载；仍验证冻结能力、步骤/任务/作用域及hash。多产物校验完整对象，批量产物不能修改所属集号。
- 人工保存先保持授权、原回执恢复和确切基线检查，再做格式校验。返修开始在事务内替换调用方自报的artifact_validation并重算上下文hash；Sidecar传给SDK实际合同，模型生成前即可读取。SDK不新建模型循环，在现有补丁完成后校验最终结果，支持多步关联字段调整；no_change保留原内容。提案入库及接受修改再次校验当前权威合同，提案/无修改完成回调还复验授权、最新运行尝试和目标基线。
- 复查发现并修复G4.85的遗漏：ContextPack入口仍拒绝非固定剧本组合的多个输出，导致已注册多产物无法真正领取。此前SDK替身测试没有走此Go入口，build/vet也不能证明该行为通过。另修正单产物重生成遗漏更新artifact.step_run_id，保持与逐集/多产物替换路径一致。
- 新增六个Go顶层用例，含无写入拒收、连续编辑/历史回执、缺失最新生成合同、返修上下文/提案/接受、授权撤回/基线变化、重生成后编辑；扩展已有合法SDK主体用例至完成阶段。**这些Go行为均未运行**。本批最后全后端build与vet退出0：`.tmp/goal-g486-build-verified.log`、`.tmp/goal-g486-vet-verified.log`。
- SDK返修专项42项通过，扩大九文件146项通过；最后全量**1552通过、0失败/错误/跳过、369.66秒**，已读取XML核对。新增21项使用固定真实SDK Runner和确定性模型或直接校验器，不调用真实网关；Go事务和Sidecar HTTP传输尚无本批运行证据。日志：`.tmp/goal-g486-revision-initial.xml`、`.tmp/goal-g486-sdk-focused.xml`、`.tmp/goal-g486-sdk-all.xml`及同名log。
- 前端四文件返修/保存/审批专项**52通过、0失败/待定**，JSON已核对；TypeScript和原四清单37步骤/21适配器检查退出0。证据：`.tmp/goal-g486-ui-focused.json`、`.tmp/goal-g486-ui-types.log`、`.tmp/goal-g486-contracts.log`。本批前端生产代码/依赖/数据库schema未改，未冒称前端全量或真实用户操作验收。
- 全部本批进程终态。未部署、未动测试机、8860/8880、用户作品/数据库/网关/凭据。下一项已确认：`approvalRevisionTarget`对同scope优先script_unit，指定集数也只匹配script_unit；通用多产物和逐集文档仍缺确切目标选择入口，不能把本批合同修复称为完整返修用户闭环。原完整范围和最终review门禁继续保留。

### G4.85 托管 Skill 多产物提交与用户确认（2026-09-10）

- 上一轮修复SDK修复模式、取得193项专项证据并更新记录，属于progress。本轮处理原已可声明但提交拒绝的不同类型多输出，不通过禁用声明缩小范围。单任务多个one产物按artifact_type键返回，ContextPack提供完整对象Schema；可选provider约束作用于整组。单输出原格式、逐集batch限制、固定SDK版本及原四流程不变。
- 新通用提交先验证完整冻结合同、身份/任务作用域、各产物Schema与已有领域规则，再在同一事务保存全部产物、上游/资产/配置/决策依赖、事件、主Task输出指针和确切版本集合审批。复用现有重生成目标绑定，直到整组保存才将计划/组转为waiting_approval，不调用会在第一份就切状态的单产物提交。不增加模型循环或数据库schema。
- 集合审批查询按singleton对应的step任务键绑定，不修改原任务标识；episode作用域保留原逻辑，并增加Run/作用域一致性条件。人工编辑第二份产物不抢占主指针，例外限定为托管单任务多输出，不改变旧剧本交接编辑保护。修改前读取已成功任务的原ContextPack、验证hash/身份、组合当前整组与候选内容并复验Schema；非法修改不创建版本或使审批过期。
- 正式回执使用已有outputs字段返回每份原版本；重试从原attempt的model版本重建，不使用后来人工编辑更新过的Task主指针充当旧结果。SDK保留托管直接输出的全部字段，让未声明字段进入正常Schema错误/修复流程，不静默删除。初次生成和输出修复仍由真实固定SDK Runner承担。
- 新共享multi-result.json只含格式夹具，不是已安装Skill。六个Go顶层用例源码覆盖真实Store入口安装临时变体、两/三份输出、重开与重复提交、资产/配置依赖、确切集合批准与Invocation完成、六种非法输出、第二份写入故障回滚后原请求重试、分别编辑主/次产物与旧审批失效、原输出回执保留、整组重生成、非法编辑不改变版本/审批。Go行为用例全部未运行；不称真实SDK/Go联合或用户数据验收。
- 初次gofmt/go命令因非登录Shell无PATH工具未执行；使用仓库既有工具链，默认系统Go缓存读取被沙箱拒绝，随后使用项目既有.gocache/.gotmp配置正常build。首次vet包列表含不存在目录而失败，改为全后端vet ./...。最终全后端build退出0，goal-g485-build-verified.log；全量vet退出0，goal-g485-vet-final.log。没有运行Go测试、改名/移位运行测试程序或绕过Application Control。
- SDK九文件专项205通过、69.21秒，goal-g485-sdk-focused.xml/log；随后完整固定Sidecar1531通过、0失败/错误/跳过、292.15秒，goal-g485-sdk-all.xml/log，已核对进程终态和XML。新增SDK十二例覆盖整组格式、缺份/多余字段/单边Schema错误、可选provider及实际Runner修复后仅一次完整提交；模型及后端为替身，不是真实网关或Go持久化证据。
- 前端生产代码未改，原集合确认用例增加artifact作用域；专项30通过、0失败/待定，goal-g485-ui-focused.json/log，TypeScript退出0，goal-g485-ui-types.log。原四清单37步骤/21适配器检查退出0，goal-g485-contracts.log，不将其算新包执行验证。限定diff检查通过，全部本批进程终态，未安装测试包或部署。
- 后续继续单产物托管Skill的人工保存格式校验：createArtifactVersionTx现只在本批集合路径复验冻结Schema，普通单产物仍只要求JSON对象；Agent返修/接受路径须继续检查。原完整范围、四流程、SDK三模式/W0至W9/GAP、user_stop待确认及真实Go/OCI/网关/浏览器/回滚门禁保持未完成。不得把本批源码接线或分层通过称为最终review完成；Goal继续active，不push、不操作测试机/8860/8880、用户作品/数据库/网关/凭据，不安装OCI或修改策略。

### G4.84 直接产物双合同与修复兼容模式（2026-09-10）

- 可选provider_result_schema_ref此前覆盖直接产物的SDK输出Schema，而Go只验目标产物：不完整产物可通过SDK、provider特有限制又可被直接提交绕开。现在ContextPack使用allOf组合已展开引用的两个Schema，正式提交校验两者；未提供或相同provider保持原Schema，旧冻结ContextPack不重写但仍检查两个原合同。共享JSON夹具覆盖单边有效、额外字段和错误信封；没有新增字段适配器或模型循环。
- 修正本批最初的无审批推断：compiler原有门禁已禁止model/batch输出直接confirmed；none原约定用于内部辅助或纯聚合，不应因此开发模型自动确认。本批在包准入拒绝pending_approval与required=false/type=none、无batch的many及重复artifact_type。两个不同类型输出仍可声明，通用Go提交尚不支持，新增准入源码用例不是该功能已实现的证据。
- 新增三项Runtime顶层Go源码用例覆盖联合合同/旧冻结合同、无provider/同provider，以及实际Store安装批量Skill后不满足provider约束的提交拒绝；新增两项Capability顶层源码检查矛盾声明和保留不同类型多输出。Go行为测试均未运行。最后全后端build、十包vet退出0，日志goal-g484-build-final.log、goal-g484-vet-final.log；原四清单37步骤/21适配器检查通过，goal-g484-contracts.log，不能算新包或真实事务执行验收。
- SDK初轮113项通过；扩大传输拒绝矩阵时误用httpx导致收集失败，检查固定openai 3.3.1的真实签名后改用其现有httpx2依赖，无安装或升级。随后114通过1失败，goal-g484-sdk-verified.xml，定位为begin_repair无条件重置structured，丢失生成阶段已选JSON模式。输出最终可提交，但重复请求已知不兼容传输，因此不弱化断言。
- 扩展原暂停/回执丢失/令牌轮换三种恢复用例的模式断言；修复前9项中6通过3失败，goal-g484-mode-red-approved.xml。默认沙箱首次读取现有pytest依赖受限，日志goal-g484-mode-red.log，未进入测试；随后同一测试通过正常权限审批执行，不修改系统策略。生产改动只移除begin_repair的模式重置，保留既有批次结束/reset、原生checkpoint、回放保护和Schema验证。
- 最后八文件扩大专项193通过、0失败/错误/跳过、46.99秒，goal-g484-sdk-mode-fixed.xml/log。包含直接响应、托管批量、Worker、状态执行/修复/后端修复/暂停/追加输入；实际固定SDK Runner执行，模型和后端为替身，模拟BadRequestError不发送网络请求。前端未变，未重跑完整分层套件，不称同版整体通过。限定diff检查通过，全部本批进程终态。
- 用户再次询问review工期，已明确仍有代码缺口且没有可靠整体剩余小时估计，不用批次/测试数量推算百分比。下一项为通用多输出的原子提交、精确审批及重生成，而不是仅放开注册或减少输出声明。原完整范围、user_stop待确认及真实Go/OCI/网关/浏览器/回滚门禁保留；不push/部署、不操作测试机或8860/8880/用户作品与数据库、不安装OCI或修改策略，Goal保持active。

### G4.83 托管 Skill 逐集批量执行接线（2026-09-10）

- 上一轮仅核对工期，为no progress；本轮恢复未完成的批量接线和验证。G4.82末尾关于首步batch的定位不完整：`validateSkillWorkflowReferences`原本已经拒绝托管包的所有批量声明，不能写成已可安装的批量Skill只缺启动。现在明确开放的是既有`episode_no`协议，不是任意数据集合/合并器。
- 托管包可使用model/batch加`worker.structured_content`；batch需串行或并行、natural_episode_order、每任务一项、preserve_success_retry_failed、一个many且pending_approval产物、必需batch_checkpoint审批，不允许平台内部preparation/task_stage或响应适配器。原审批scope省略时继续采用编译器默认值，不错误拒绝等价声明。启动、ContextPack和提交复用同一支持判断及原Run/Task/Artifact/Approval机制，直接Schema不包旧artifact信封。实际每次模型执行仍由固定SDK Runner承担。
- 抽取既有单产物资产/配置/决策依赖写入，并复用于逐集提交，保留episode作用域；资产依赖仅对直接Skill产物启用，配置与决策同原事务落库。保留任务身份、上下文hash、版本、工具结果与审批校验。目标集数在服务端限制1至MaxAssetSetEpisodeNo=10000，防止包配置触发无界任务创建；未改变原四流程清单。
- 新包`fixtures/skills/batched/episode-review-workflow`含SKILL.md、平台manifest/workflow、三份Schema和Prompt；与原stateful夹具根分开，不影响其单包断言。它支持资产或已选产物来源、逐集报告和通用版本集合确认。包未安装到`.agents`或用户范围，未使用用户作品/数据库。
- 新增两项Capability顶层源码覆盖串并行/默认审批scope准入和十五种不支持声明反例；六项Runtime顶层源码覆盖真实Store入口安装/配置/启动、暂停重开、原成功项与资产/配置依赖保留、耗尽自动重试后手动只重试失败项、并行乱序结算、串行前序门禁、错集拒绝、精确批量审批/幂等及Invocation完成、目标集数边界。所有Go行为用例均未运行；首次静态检查发现新增并行测试多余变量，已修正，不修改保护或绕过Application Control。
- SDK新增一项包JSON Schema验证及三个实际Runner逐集输出用例。先前项目虚拟环境缺少jsonschema，未进入产品断言；改用已核定有固定SDK的系统Python及现有pytest目录，经正常读取审批执行。第一轮运行1通过3失败，因模拟注册表正文与冻结任务不一致，被原保护拒绝；修正夹具后连同Worker/状态恢复专项107通过、0失败/错误/跳过、40.34秒，`.tmp/goal-g483-sdk-focused.xml`/log。使用真实SDK Runner和包Schema，但模型及注册/提交后端为替身，不是Go安装或真实模型联合。SDK/OpenAI版本与生产Sidecar未改，未安装依赖。
- 前端通用batch版本集合确认新增一项，专项29通过、0失败/待定，`.tmp/goal-g483-ui-focused.json`/log；确认绑定原版本集合，不读取集合为单份Artifact、不出现剧本专属设置。TypeScript退出0，`.tmp/goal-g483-ui-types.log`；没有生产UI变更，不启动Vite或用户端口。末版全后端build退出0，`.tmp/goal-g483-build-complete.log`；末版十包vet退出0，`.tmp/goal-g483-vet-verified.log`。原四清单37步骤/21适配器校验退出0，`.tmp/goal-g483-contracts.log`；不将它算成新包语义执行验证。
- 当前所有命令已终态。未重跑未变更的前端/SDK全量，不把本批专项加上历史全量拼成同版整体通过。已见到可选provider_result_schema_ref先于直接输出默认分支，而正式提交仍校验目标产物；此外非批量many、多输出及无审批声明的注册/提交边界尚须继续核对。原四流程、全部W0至W9/GAP、user_stop待确认及真实Go/OCI/网关/浏览器/回滚门禁保留；不push/部署或操作用户环境，不缩小Goal、不签署最终review。

### G4.82 Runtime链式调度与真实完成边界（2026-09-10）

- 上一轮G4.81文档同步和合同复验改变了权威记录，为progress。本轮从实际executor.Registry、server装配、动态Registry可用性解析和SDKTaskWorker领取名单核对执行入口。未注册执行器已有EXECUTOR_UNAVAILABLE门禁并在动态刷新保留，不把compiler仅检查非空错误认定为整个系统没有检查。
- 确认ResumeRun只执行一次Runtime步骤后对所有后继调用Worker规划；连续Runtime后继可能报错或成为无人领取任务，已自动开始的质量审核也因不是pending被错误回滚。改为在原事务中逐步执行Runtime并重读Run，核对确切当前步骤及pending/paused状态；实际waiting_approval/completed返回原命令结果，已running的质量审核保留既有任务，不重复规划。Worker入口、身份/权限、原请求幂等、失败分支封存校验及审批保护沿用原实现。
- 单次命令的连续Runtime链只执行每个声明步骤一次，遇到同名步骤的新pending实例时提交当前进展并保持paused，下一次继续仍须独立命令；不改变用户取消，也不声称任意有状态循环已经端到端可用。后继执行或事件/回执存储出错仍回滚整个事务，不提交前半条链。
- 来源清单、剧本上下文、体量判断此前丢弃终点布尔值，剧本聚合此前忽略后继并固定返回Completed。现全部传播真实推进结果，Resume以事务内Run状态决策。run.completed从聚合器移到共同终点提交处，只在实际状态迁移时生成，非终点聚合不再发完成事件；重复原回执不重复事件。
- 新增runtime_dispatch_test.go五个顶层用例源码，覆盖连续Runtime后接单个模型任务和领取、Store重开后原回执、来源/体量作为终点、独立继续审批/体量审批、循环让出、第二步故障回滚及同键重试、聚合后接模型/已规划质量审核/真实终点。控制流变体使用明确修改的编译定义，聚合用既有已确认产物夹具，不是新Skill安装、真实模型生成或原四流程完整E2E。静态复核纠正新夹具调用GetRun而不是GetRunSnapshot及将领域批次误当作单任务的问题，没有修改原四份清单来迎合测试。所有新增Go行为用例未运行。
- 末版全后端build退出0，`.tmp/goal-g482-build-final.log`；最后十包vet退出0，`.tmp/goal-g482-vet-final.log`，本批增加executor包静态核查。四原清单37步骤/21适配器合同校验退出0；限定diff与新文件尾随空白检查通过，全部本批命令终态。前端/SDK/依赖/schema未改未重跑，不拼接历史分层结果为同版联合通过，没有执行受限Go测试或绕过Application Control。
- 下一项已确认startManagedWorkflowRun仅接受workerExecutableStepKind，因其排除batch而拒绝批量首步；后续须连同现有批量规划/领取/提交协议核对，不能仅允许启动后留下无法交付的任务。原四流程、全部W0至W9/GAP、user_stop待确认及真实Go/OCI/网关/浏览器/回滚门禁保留。未push/部署或操作测试机、8860/8880、用户作品/数据库/网关/凭据、安装OCI或修改策略；Goal继续active，不签署最终review。

### G4.81 Workflow条件注册前校验（2026-09-10）

- 修复Go编译器和离线Node合同检查只要求when非空的问题。原十种条件名与manifest校验schema枚举对齐；未知名称、大小写或空白错误不自动纠正。同条件不同目标和failure自环拒绝，同目标重复条件及字符串completed简写保留。user_stop仍为原范围内已知但未开放条件，注册时明确报错，不以拒绝使用冒充实现，也不改变CancelRun语义。
- Go TransitionRef对象解析使用严格结构化解码，拒绝额外键；保留已编译快照When/To字段的兼容读取。核心注册和portable Skill workflow_ref均复用compileManifest。新增五个Go顶层测试源码核对条件表/schema、编译拒绝与兼容、快照往返、额外字段以及实际Skill目录检查和扫描诊断；全部未执行，不把源码或build/vet当行为证据。
- 默认沙箱首次Node测试因spawn EPERM未进入产品断言，日志`.tmp/goal-g481-contract-admission-red.log`。同类命令经正常权限审批后，修复前7项中1通过6失败，12.33秒，`.tmp/goal-g481-contract-admission-red-approved.log`；六种非法清单被旧校验器错误接受。额外字段测试在该红测之后补入，不声称其也有修复前失败证据。
- 最后Node合同完整回归40通过、0失败/取消/跳过/待定，87.14秒，`.tmp/goal-g481-contract-regression-final.log`；本次接续重读日志确认完整统计，原会话句柄已结束。末版全后端build和九包vet退出0，日志`.tmp/goal-g481-build-final.log`及`.tmp/goal-g481-vet-final.log`。前端/SDK/依赖及数据库schema未改未重跑，不拼接历史分层证据为同版联合通过。
- 本批不启动新产品方向。后续核对动态Workflow清单与实际调度入口的一致性，当前只是待查方向。原四流程、完整能力清单及真实Go/OCI/网关/浏览器/回滚验收继续保留；不绕过Application Control，不push/部署或操作测试机、用户环境、数据库、网关与凭据。Goal保持active，不签署最终review。

### G4.80 失败后继、原执行保留与恢复校验（2026-09-10）

- 上一轮仅核对并回复工期，未改变代码，按no progress处理；本轮重新检查当前失败结算、批次完成回调、原生工具账本和Resume路径后完成实现。原Goal范围、无部署和系统安全策略边界不变。
- 新workflow_failure_transition复用已固定的能力版本、已有步骤/决策快照/事件表。failStepRunAndProjectTx在记录step.failed之后选择failure；自动重试仍优先，保留成功项的批次等待全部结算，最后一个成功项的提交同样能触发失败结算。只有合法后继才创建独立pending步骤并令Run paused，不创建任务/尝试、不调用模型；原任务和尝试仍failed，Skill Invocation不提前终结。取消仍彻底结束并同步Invocation，不走收尾分支。
- 失败后继输入从失败步骤原输入及其依赖链选择，要求当前已确认版本，单份歧义拒绝，不读取整个Run的任意同类型产物。决策保存确切来源/目标步骤、封存输入ID、失败项及成功数、旧checkpoint尝试ID、已完成写工具调用ID/SDK ID/参数hash与摘要/结果hash与摘要。私有SDK RunState和原provider_output不进入新上下文；checkpoint仅留在旧尝试。现有ContextPack提供该封存决策给新步骤，不假装恢复旧执行。
- 未结束任务/尝试/审批、独立重生成计划、未知写入、仍starting/running/lost的PTY进程、保护/状态完整性失败、过期输入和写锁变化均不创建后继。错误目标/歧义/缺输入等在只读准备阶段返回，并记录workflow.failure_transition_blocked后继续保存原失败；真实数据库或写入故障回滚同事务，不提交孤立步骤。交接证据有1MiB上限，超限保留原记录并停止，不截断为可执行事实。
- 首次Resume前核对确切决策ID/hash、来源和目标步骤、输入JSON、任务结算事实和工具结果；旧决策/来源改变时不排队。之后目标步骤自己的暂停沿原SDK恢复路径。HTTP的失败分支状态变化/未结算/需检查映射为409。后端用户过程投影和前端ProcessHistory显示待继续/无法继续，保留失败数量，不显示为原步骤成功；真实浏览器/SDK联合未验收。
- 新增七个Runtime顶层用例源码：自动重试耗尽/跨Store重开/重复上报/显式继续与真实ContextPack；串行和并行批次最后成功项结算；非法清单/输入/写锁/guardrail失败仍落库；合成关联Invocation直到Run取消才终结；工具批准/拒绝/已开始/已完成/未知结果与保留checkpoint；恢复损坏/取消拒绝；决策写入真实故障注入及整体回滚。Invocation关联为明确状态夹具，注册表变体为编译后测试夹具；不称真实Skill安装/模型/OCI E2E。扩展现有HTTP状态码用例；全部Go行为测试未运行，不绕过Application Control。提取原视频用例的运行中准备函数，原部分继续测试主体保留。
- 末版全后端build退出0，`.tmp/goal-g480-build-complete.log`；九包vet退出0，`.tmp/goal-g480-vet-complete.log`。前端专项2文件78项通过、13.37秒，`.tmp/goal-g480-ui-focused.json`/log；最后固定前端全量65文件900项通过、0失败/待定、268.80秒，`.tmp/goal-g480-ui-all.json`/log，已重读JSON与终态核对。TypeScript退出0，`.tmp/goal-g480-ui-types.log`；隔离Vite退出0、2.38秒，输出到经核定不存在且位于工作区的`.tmp/goal-g480-ui-dist`，保留576.64kB主chunk提示，`.tmp/goal-g480-ui-build.log`。四原清单37步骤/21适配器合同校验退出0，`.tmp/goal-g480-contracts.log`。SDK/schema/依赖未改未重跑，全部本批进程已终态。
- 下一项已从compiler.go确认：transition.when仅验证非空，没有按原合同拒绝未知条件，拼写错误可通过注册但执行时才UNMATCHED；本批未修改编译器。user_stop仍等待先前提出的语义确认，不将未回复算作同意。完整范围和真实Go/OCI/网关/浏览器/回滚门禁保留，不签署最终review，不给未经核定的整体剩余工期；未push/部署或操作用户环境。

### G4.79 用户继续确认、Runtime与质量审核投影（2026-09-10）

- 上一轮G4.78包含生产代码、用例、build/vet和文档更新，为progress。本轮继续原完整Goal，不接续已回答的工期问题。已核对原合同及控制命令；user_stop是否允许仅整理已有结果的收尾步骤已异步询问，尚未收到答复，原CancelRun继续彻底取消。
- 自动完成事实无匹配而存在user_continue时，先验证该条件目标唯一且存在，再复用run_decision_snapshots保存workflow_transition_request，记录来源/目标步骤、Run输入快照ID及确切已确认版本。审批subject_ref绑定这个决策ID、版本及payload hash，approval_subject_versions同时绑定版本顺序/范围供修改冲突检查。请求创建不等于用户已经继续，不通过模型文字推断授权。
- 用户批准通用流程确认时，复查Run当前步骤/状态、决策来源/hash/版本、固定目标、当前confirmed产物及完整审批版本集合。客户端额外目标或扩写策略拒绝；确认、推进、事件及原键回执同事务，失败回滚，重试不重新建后继。复用原权限和幂等入口；原扩写确认scope=transition仍走原路径，不把新确认当作volume_fit策略。
- Runtime完成后若创建该待确认请求，Resume返回真实waiting_approval而不因没有后继误报执行失败；请求仍未批准时普通Resume拒绝。质量审核的专门推进路径也接同一确认机制，绑定审核的正文及交接等输入，确认前不直接聚合。保留质量审核原有唯一聚合后继约束，不宣称任意图结构均可执行。
- Workspace registry新增优先于旧transition扩写规则的workflow_transition/action_list投影，前端复用已有确认按钮，不增加产品编排或自定义代码加载。新增组件用例核对无需读取决策为Artifact、不显示扩写输入框、点击前不提交，点击后提交确切审批及approve且无扩写payload。
- 新增四个Runtime顶层测试源码，覆盖独立确认、普通Resume拒绝、Store重开、同键不重建后继、客户端指定目标/损坏决策/过期产物/缺失版本绑定/取消后旧确认拒绝，Runtime体量步骤等待继续，以及质量审核版本绑定和显式继续。最后一项使用明示的既有状态夹具，不是完整模型审核或生成E2E。新增一个workspaceview顶层用例核对通用确认和旧扩写的路由。所有Go行为用例均未运行，没有绕过Application Control。
- 最后全后端build退出0，`.tmp/goal-g479-build-final.log`；九包vet退出0，`.tmp/goal-g479-vet-final.log`，增加workspaceview包。前端专项2文件33项通过、10.06秒，`.tmp/goal-g479-ui-focused.json`/log；最后全量65文件898项通过、0失败/待定、232.73秒，`.tmp/goal-g479-ui-all.json`/log，已重读JSON核定。TypeScript退出0，`.tmp/goal-g479-ui-types.log`。四清单37步骤/21适配器合同校验退出0，`.tmp/goal-g479-contracts.log`。前端仅测试源码改动，未重跑Vite；SDK/schema/依赖未改，未重跑SDK，不拼接为完整同版联合证据。所有本批命令均已终态，限定diff检查通过。
- failure条件仍未接：现有失败处理保留自动重试、批次成功结果及工具重放风险，不能把FailExecutionAttempt调用直接当作可以执行后继的授权。后续从整步结算和恢复入口补接，不把用户停止改为启动新任务。原四流程、W0至W9/GAP及真实Go/OCI/网关/浏览器/回滚门禁保留，Goal继续active；未push/部署或操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI、修改策略或启动浏览器。

### G4.78 不完整材料事实、部分审批绑定及编辑接续（2026-09-10）

- 用户继续询问距离完整review结束多久；核对当前Goal和原清单，说明仍有代码缺口与验收条件，未编造交期或把分层编译当作交付。按原授权继续，不新建或缩小Goal。
- sourceIncompleteMaterialConfirmedTx读取本Run确切封存输入及对应素材集合版本，核对作品、封存状态和真实缺集；仅mode=continue_incomplete且confirmed_by=user产生条件事实。没有缺集返回false，不从模型文字或仅有mode推断用户确认。
- partialBatchContinuationForStepTx按run/step读取当前任务和最近封存部分决策，校验source_ref、payload hash、成功数与失败键；使用现有approval.requested的decision_snapshot_id核对唯一审批关联，再核验当前approved状态、获批版本集合hash和当前confirmed版本。聚合额外核对选用版本恰为该来源步骤的版本集合，不读整个Run最后一个不相关决策。无失败时忽略历史部分决策，但旧聚合输入缺少已补齐输出仍报冲突。
- 聚合从实际video_script_unit归属步骤读取失败项，去重排序并剔除本次输入已经提供的集号；无效失败集号不再静默丢弃。成功/审批推进和聚合入口现在可产生incomplete_material_confirmed事实，不新增前端编排、模型循环或数据库schema。
- 静态复查发现新增绑定会阻断编辑待确认的部分结果：编辑创建替代审批但原决策仍指向旧审批。现编辑前验证该确切pending审批和原任务/版本集合；替代审批同事务创建新的部分决策与审计关联，保留缺集标题/原因并记录replaces_approval_request_id。普通非部分审批不进入该绑定路径；新版本仍需用户显式批准，不继承旧批准。此处覆盖不需要script_handoff刷新的版本集合编辑，不声称所有重生成/交接刷新路径已完成真实验收。
- 新增四个Go顶层测试源码，覆盖部分批次及聚合的实际审批/继续调用链、封存缺集分支、17种当前决策/审批/任务/版本状态、5种封存输入状态，以及连续两次编辑保留提示/关联、旧审批拒绝、新审批后继续。全部未执行；all-repaired是明示的数据库状态夹具，不是实际重生成证据。原视频部分结果用例仅提取公共准备函数，保留已有断言。
- 最终修改固定后全后端build退出0，`.tmp/goal-g478-build-complete.log`；最后八包vet退出0，`.tmp/goal-g478-vet-complete.log`。文档更新后合同校验退出0，四清单37步骤/21适配器，`.tmp/goal-g478-contracts.log`；未重跑未修改的Node合同测试，不把校验器当作Go行为证据。此前initial/final日志仅为中间版本，不冒充最终证据。没有执行Go测试程序或更名、换路径、修改策略绕过限制。前端/SDK/schema/依赖未改未重跑，不拼接旧分层证据为同版联合通过；本批所有命令均已终态，限定diff检查通过。
- 仍未完成user_continue/user_stop/failure原条件边、四流程完整用户交付、完整W0至W9/GAP及真实Go/OCI/网关/浏览器/回滚门禁。未push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI或修改系统策略；Goal保持active，不签署最终review。

### G4.77 Workflow条件匹配与原合同缺口核定（2026-09-10）

- 上一轮G4.76完成代码、回归和文档更新，属于progress。本轮继续原Goal，未接续工期问答或缩小完整范围。从四份实际清单、推进器、审批及重生成调用链发现`Next[0].To`忽略条件；内置体量分支同目标不代表上传Skill分支能正确运行。
- 成功推进提取确定性条件匹配，不按顺序选边。Runtime提交入口传入已验证的approved/体量充分/扩写确认/改编选择及配置确认事实；普通完成保持completed。批次分支用当前事务查询任务总数与succeeded数，必须非空且全部成功。多个匹配条件同目标可合并，不同目标或空目标报WORKFLOW_TRANSITION_AMBIGUOUS，无匹配报WORKFLOW_TRANSITION_UNMATCHED，HTTP保留409及明确错误码。
- 单选/改编/扩写/普通及整步审批、重生成计划结束后的回接、质量审核通过及保留风险均已接同一判断；原授权、固定版本、审批快照、产物依赖和回执事务保持。没有新增模型自选Step、表达式执行器或前端业务分支，没有修改四份清单/schema或数据库结构。
- 新增五个Runtime及一个HTTP顶层测试源码：分支顺序交换、成功/审批/体量/改编事实、同目标合并、缺失/未知/歧义拒绝，四原流程实际入口事件集合，审批错误无状态提交与原回执不重复建后继，体量充分与确认扩写走不同目标，混合/空/失败/取消批次不能触发全部成功，以及HTTP冲突可见。全部未执行，未绕过Application Control；不得把这些源码列为通过。测试中的原流程事件不从清单when反推，避免自证循环。
- 全后端build退出0，`.tmp/goal-g477-build.log`；最后八包vet退出0，`.tmp/goal-g477-vet-complete.log`，包含capability和HTTP/Runtime测试编译。四清单/37步骤/21适配器合同校验退出0，`.tmp/goal-g477-contracts.log`。Node合同测试首轮被子进程spawn EPERM拦截，`.tmp/goal-g477-contract-tests.log`；同命令经正常权限审批后32通过、0失败/取消/跳过/待办、62.87秒，`.tmp/goal-g477-contract-tests-approved.log`。这是既有合同校验器回归，不是新增Go分支的执行证明。未改变测试运行器或系统策略。
- 原合同第17节还列有user_continue/user_stop/incomplete_material_confirmed/failure；复查当前生产调用未发出这四种推进事实。原控制命令或失败状态存在不等于条件边已实现，已在合同和R1明确登记为原范围代码缺口；后续补接不得让停止操作隐式启动新生成。当前拒绝无匹配只是修复错误选边，不冒充完整条件Workflow交付。
- 本批前端/SDK/schema/依赖未改未重跑，分层旧证据不拼成同版联合。所有本批进程已终态；未push/部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI、修改策略或启动浏览器。原四流程、完整能力清单与真实环境门禁继续，Goal仍active，不签署最终review。

### G4.76 分集确认与执行方式同事务（2026-09-10）

- 复查原四流程审批路径发现：前端“确认并自动生成剩余集”先调用模式修改再提交审批；第二步失败时模式已提交，原键重试也会额外调用模式接口。后端版本集合确认此前不消费该模式payload。这是原有用户命令的部分成功缺陷，不是新增产品功能。
- 后端将原模式变更提取为既有事务内helper，复用Run固定能力快照、模式及Run终态检查；审批在确切版本/产物/交接/hash验证后写模式快照，确认产物、审批回执、事件及幂等仍在同一事务，后续失败一起回滚。模式事件和审批resolution绑定config_snapshot_id。独立模式命令保留API，接既有事务内授权及原回执目标核验，不新增schema。
- 前端删除审批前单独切模式的请求，原键直接携带episode_execution_mode到审批。仍在确认后读取确切Run并执行原有显式继续命令，未声称确认和继续也已合并。省略payload、JSON null和空对象保持旧客户端兼容；明确传非法/空模式、非逐集scope或非对象payload拒绝。需同版本前后端验收，不宣称旧后端会消费新payload。
- 新增实际App测试正常与丢回执两分支，验证不调用独立模式API、失败不继续、重试保留原参数及key、最终只继续对应审批Run；新增HTTP传输body测试。修复前两个App反例失败见`.tmp/goal-g476-atomic-ui-red.json`/log。首轮专项199通过1失败为新传输测试误写路径`/resolve`，改为实际`/resolutions`，产品API未修改；修正后3文件200通过、56.56秒，`.tmp/goal-g476-approval-ui-final.json`/log。
- 新增Go行为源码含5组成功/无附加payload/重开Store后原回执不覆盖后来模式，以及10组拒绝与故障回滚，包括审批写失败、后续任务失败、Run状态变化；扩展既有公开控制授权矩阵覆盖独立模式命令。全部未运行，不绕过Application Control。初次vet发现提取夹具误解引用Approval值，已修正；最终全后端build退出0，`.tmp/goal-g476-build-final.log`，七包vet退出0，`.tmp/goal-g476-vet-complete.log`。
- 最后固定源码前端全量65文件897项通过、0失败/待定、272.69秒，`.tmp/goal-g476-ui-all.json`/log，已重读JSON及进程终态。TypeScript退出0，`.tmp/goal-g476-ui-types.log`；Vite退出0、1.83秒，`.tmp/goal-g476-ui-build.log`，仅新建已核定位于工作区内的`.tmp/goal-g476-ui-dist`，保留576.42kB主chunk提示。本批SDK/schema/依赖未改未重跑，所有命令已终态。
- 未push/部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI、修改安全策略或启动浏览器。继续原四流程完整交付及SDK三模式/W0至W9/GAP核定；受阻验收与代码缺口分开，Goal仍active，不以本批修复签署最终review，也不再给无可核定依据的整体小时估计。

### G4.75 异步消息合同、四流程投影与失败任务保护（2026-09-10）

- 用户再次询问距离review结束多久；核对active Goal、当前剩余表和源码后，未给未经核定的小时承诺，继续原范围。没有把本批完成视为最终review，没有启动新目标或缩减SDK/Codex-like用户任务。
- 只读复核公共`createMessage`新接受/原回执均返回202 AgentTurn，SDK`_build_commit_payload`与`commit_agent_action`提交控制决策，Runtime`buildProposedActionTx`绑定精确素材、请求消息和封存视频配置。前端`load`、提交事件和断线轮询统一读取Workspace Projection。保留内部Go/SDK的MessageExchange提交合同，仅删除前端公共API的联合类型、无用DTO和旧同步后处理。
- 旧同步回执现在按不匹配回执处理：保留原消息、原键及结果未知状态，不注入伪成功消息、不补发input-binding/configuration。真实App验证再次发送仍用原请求，并在合法AgentTurn回执后清理。用户显式配置、材料选择和确认启动保留；并未删除公共input-binding API或后台内部提交能力。
- 原请求直接返回committed时此前既不触发轮询也没有新SSE事件，四流程均复现最终回复/配置卡缺失。现在对committed/failed/cancelled回执主动只读刷新，仍走原归属校验及迟到结果保护。四流程各覆盖完成事件、断线轮询及已完成回执三分支，核对服务器材料/配置不被客户端覆盖、未操作前不启动、显式配置/启动传精确提案。
- 移除选择Skill发送新消息前的自动cancel_run。Runtime原有生成/动态workflow启动事务在确认合法提案后释放失败Run写锁，保留失败历史；发送本身不应取消旧任务或因没有取消按钮而被拦截。修复前5个新增反例失败，修复后覆盖新发送/旧请求恢复，以及inline/background/stateful消息被拒绝时不取消或启动其他Run。Go源码和行为测试本批未改未运行，既有后端替代任务用例只做只读核对，不冒充已执行。
- 首轮专项4文件221通过/4失败、45.22秒，`.tmp/goal-g475-ui-focused.json`/log；失败为测试漏匹配时长标签中的单位，修正选择器而非产品。终态反例4失败、6.61秒，`.tmp/goal-g475-terminal-red.json`/log；修复后同4文件229通过、48.41秒，`.tmp/goal-g475-ui-focused-fixed.json`/log。首轮全量888通过/1失败、267.19秒，`.tmp/goal-g475-ui-all.json`/log；Skill管理用例把尚未出现清理按钮误当成已清理，改为直接等待beforeunload警告解除，没有改生产Skill保护或延长超时。
- 提前取消反例5失败/2通过、10.60秒，`.tmp/goal-g475-precancel-red.json`/log；两个反例命令使用名称过滤，其跳过项不计覆盖。最后扩大专项5文件316通过、74.70秒，`.tmp/goal-g475-ui-expanded.json`/log。最后固定源码全量65文件894通过、0失败/待定、243.78秒，`.tmp/goal-g475-ui-all-final.json`/log，已重读JSON核对，期间前端源码/测试未改。
- 末版TypeScript退出0，`.tmp/goal-g475-ui-types-complete.log`；隔离Vite退出0、1.79秒，仅输出新建`.tmp/goal-g475-ui-final-dist`，`.tmp/goal-g475-ui-build-final.log`，保留576.68kB主chunk提示，未提高阈值。限定diff检查通过，所有本批命令已终态。
- 本批只改前端源码/测试及上述记录。Go/SDK/schema/依赖未改未重跑；未push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据，未安装OCI、改系统策略或开启浏览器。原四流程后续完整生成、审批、返修、交付和三模式真实验收仍需核对，原W0至W9/GAP范围保留，Goal active，不签署最终review完成。

### G4.74 消息接受回执、哈希兼容及前端恢复边界（2026-09-10）

- 继续原Goal，上一轮G4.73补齐全量前端证据并确定后端缺陷，属于有效进展。读取当前接受/队列/幂等/授权代码后，确认后端初始接受回执与当前排队正文是不同对象：队列编辑会修改agent_turns.request_hash，不能拿该可变字段证明最初提交。
- 新LookupAgentTurnSubmission只读事务复核当前用户、工作区成员状态/角色、作品未删除和对话归属，命中create_message记录后核对回执ID、作品/工作区/用户/对话、创建时间、接受状态、可用的submission_id以及执行表原键。直接Accept缓存读取复用同一绑定，另拒绝错误scope、显式服务身份和Agent活动冒充用户。缺失、损坏或未完成回执不作为新请求处理；过期时间也不抹除已有接受事实。此处不代表已运行Go行为测试。
- HTTP先恢复原回执，再对真正的新请求执行Preflight、rollout、素材补齐及接受。恢复返回原执行当前状态，不通知manager或重放排队编辑。新请求的message-request-v2哈希固定于自动素材补齐前，现有create_message键空间与唯一约束保留，不新增schema。旧协议先核对原哈希；只有原协议可能自动绑定的单份无展示/隐藏/容器元数据素材，才从不可变回执重建旧规范化请求并验证旧hash，不读取当前素材、不用编辑后正文、不降级匹配新版hash。旧协议原本不区分同一规范化请求的显式/隐式素材，不声称其证明了最初HTTP原始字节。
- Go新增只读查询、队列编辑/关闭重开、撤权/禁用/跨用户、回执损坏/错键/缺失、哈希差异和固定协议向量用例；HTTP覆盖新旧协议在素材变成多份、Skill停用、manager未启动/rollout关闭后读取原执行，且新请求仍拒绝。fixture只准备接受调度器而不运行模型，所有新增Go行为源码均未执行。初次及最后全后端build和七包vet退出0，最后日志`.tmp/goal-g474-build-final.log`、`.tmp/goal-g474-vet-final.log`；没有运行测试二进制或绕过系统策略。
- 前端sendSaved显式区分原请求恢复与首次发送；恢复不先取消当前失败Run，4xx仍保留未知状态。AgentTurn回执核对用户/工作区/作品/对话、状态、时间、正文和附件结构，在WebCrypto可计算时核对原submission_id；没有WebCrypto的旧上下文仍只有范围/结构核对，不声称具备同等原键证明。非法/缺失回执在更新UI或清除原记录前拒绝。停止后的迟到成功不覆盖重试，旧回执不倒退已见较新执行状态。旧MessageExchange分支本批只补基本格式拒绝，未冒充其具备AgentTurn同等关联证明。
- 前端专项初轮4文件185通过24失败，`.tmp/goal-g474-message-ui-initial.json`/log；有效回执夹具缺少新后端已有的submission_id，残留未知请求又影响后续共用作品用例。改用原请求键同步生成与后端协议一致的测试回执，新增故障用例独立作品，不降低生产校验或断言。复测4文件209通过、43.51秒，`.tmp/goal-g474-message-ui-fixed.json`/log。最后源码固定后全量65文件877项通过、0失败/待定、218.78秒，`.tmp/goal-g474-ui-all.json`/log，已重读JSON核对；TypeScript退出0，`.tmp/goal-g474-ui-types-final.log`；隔离Vite退出0、2.04秒，产物只在新路径`.tmp/goal-g474-ui-dist`，`.tmp/goal-g474-ui-build.log`，579.32kB主chunk保留提示。全部本批命令已终态。
- SDK及固定依赖、schema未变，未重跑SDK。没有修改用户数据库、运行服务、8860/8880、网关或测试机；未部署。无schema变更不等于旧二进制可恢复新哈希记录，配套代码/数据回滚门禁仍未验证，不能跳过或宣称通过。后续继续W5/W9前端流程真源收敛、原四流程和完整能力清单，整体Goal保持active。

### G4.73 首条及普通消息原请求恢复与后端重试缺口（2026-09-10）

- 用户询问距离review完成多久。核对active Goal、当前代码与记录后，继续明确仍处于跨模块审查和缺陷修复，不是只剩最终签字；没有重新给出未经核定的小时承诺，也没有缩小原范围。
- 新messageSubmission保存schema/归属/原UUID键、原客户端ID、完整Skill版本快照、附件、封存视频批次及提交视图/选区，元数据上限2MiB。按工作区/用户/作品/对话隔离；prepare不覆盖不同记录，claim同步写pending后才发请求，clear仅删除确切原记录。sessionStorage只保证当前标签页存续期间的恢复，不声称跨标签页或关闭浏览器后可靠保存。
- 新建作品不再在发出首条消息前删除交接记录，普通Composer复用同一路径；真实App传入认证归属，身份变化重建路由组件，乐观消息缓存也按用户/工作区隔离。重建未知请求不会自动提交；显式重试保留原参数、版本、上下文及客户端ID。首发前所选Skill失效不退回通用对话，未知结果不默认为失败；停止不清记录，迟到完成不能删除下一条消息。按新请求编辑需确认原请求可能已经执行。
- 存储错误锁写但保留原记录，viewer不发送，已确认响应后本地清理失败只重试清理。旧无归属handoff不展示内容、不猜测迁移版本/身份、不自动发送或删除。恢复反馈移到独立布局行，保留原输入/按钮布局；未做实际浏览器或截图验收。
- 初轮新增恢复测试3文件62通过3失败，`.tmp/goal-g473-message-recovery.json`/log；失败注入未拦截实际sessionStorage，改为绑定真实storage方法的全局包装并断言注入调用，不放宽生产故障门禁。修正后恢复/API/首消息/Composer专项4文件103通过，`.tmp/goal-g473-message-recovery-fixed.json`/log。实际App扩大初轮151通过1失败是render与等待置于同一act导致提交未发生；分开后2文件152全部通过，`.tmp/goal-g473-message-app-fixed.json`/log。实际App测试仍使用mock API，不是Go联合验收。
- 最后固定前端全量65文件860项通过、0失败/待定、214.98秒，`.tmp/goal-g473-ui-all.json`/log，已重读JSON核对。TypeScript退出0，`.tmp/goal-g473-ui-types-final.log`；隔离Vite退出0、1.56秒，产物仅新建`.tmp/goal-g473-ui-dist`，`.tmp/goal-g473-ui-build.log`，主chunk577.85kB保留体积提示。所有本批测试/构建进程已终态，未启动服务或部署。
- 尚未修复的后端问题：`httpapi.createMessage`先调用PreflightMessage验证当前注册版本/素材/选区，再自动绑定单份素材，最后才计算create_message哈希并调用AcceptAgentTurn。后者虽先查幂等记录再验当前输入，HTTP前置校验仍可拦住已接受消息的合法重试；自动素材变化也可改变原键对应哈希。下一步需在当前身份/权限复核后读取确切原回执，固定原客户端请求身份，并保守处理已存在的旧哈希协议，不能猜测旧请求曾有无附件而重放。该实现缺口独立于外部测试阻塞。
- Go/SDK/schema本批未改未重跑；Go行为禁令、不操作用户环境/数据库及未授权浏览器/OCI/网关门禁保留。全局review未结束，Goal保持active。

### G4.72 Skill管理原请求持久恢复与存储故障回归（2026-09-10）

- 用户再次询问距review结束多久。核对active Goal、当前源码/日志及原范围后，没有重报未经核定的总工期；继续当前补齐与回归，不将本批通过等同最终review。G4.71曾记录未实现的Skill整页恢复现在进入生产源码，首条消息及其他原业务链路不能因此算作全部恢复完成。
- 新skillMutationJournal使用原生IndexedDB，按工作区/用户保存八类原管理命令，ZIP存实际ArrayBuffer和文件元数据，JSON元数据2MiB/ZIP20MiB上限，双方SHA256、schema、身份、版本/事件及目录目标验证。先等待claim事务提交才发HTTP；并发claim不覆盖已有命令，requireCurrent不重新创建已清除的旧命令，settle不删除不同的后续记录。原记录不自动过期，不静默回退内存或删除损坏记录。
- SkillManagement要求确切userID/workspaceID，实际App按两者key重建；恢复只展示原命令，不自动发送，重试不采用新页面的范围选择或远端新版本。待确认文案显示动作、Skill、原作用域和目标ID，版本切换显示原目标版本。成功/确定拒绝但本地清理失败时，同页重试只清记录；降级为viewer仍允许该本地清理，不放开远程写入。完整重载后未清除记录保守按未知处理，不把内存settled标记当成持久证明。
- 复现并修复跨realm ArrayBuffer被instanceof误判；改用ArrayBuffer内部byteLength访问验证，仍核对长度和hash。新版回归又复现显式刷新时running锁误阻恢复已清除记录、角色变更误拦纯本地清理，以及API初始化在sessionStorage写回失败后换掉已读取ID。分别增加独立mutationInFlight判断、仅已确认原命令的本地清理分支，以及保留读取成功的原ID。存储读取失败仍可显示已读取目录，但所有新Skill写入口关闭。
- 新测试依赖仅fake-indexeddb 6.2.5，校验registry缓存的版本、integrity及Node>=18约束；无运行时或SDK依赖升级。普通pnpm调用退出0但未安装/写锁文件，未计成功；经正常权限审批后用隔离npm前缀安装，禁用scripts，复用该已下载包进入本仓库node_modules，手动只增加相应package/lock条目。日志`.tmp/goal-g472-test-dependency.log`和`.tmp/goal-g472-isolated-dependency.log`保留。没有安装OCI或修改系统策略。
- 初轮journal/UI62通过20失败，`.tmp/goal-g472-journal-ui-initial.json`/log；FileReader复现1失败63跳过仅作诊断，`.tmp/goal-g472-journal-dom-repro.log`。跨realm及旧异步等待修正后84通过1失败，`.tmp/goal-g472-journal-ui-fixed.json`/log；余项是错误先展示、异步本地清理未结束时按钮仍为处理中，改为等待实际禁用按钮恢复，不降低刷新不重发断言。扩大恢复/故障回归79通过6失败，`.tmp/goal-g472-recovery-regression.json`/log，真实缺陷修复后专项4文件106项全部通过、26.73秒，`.tmp/goal-g472-recovery-fixed.json`/log。
- 最后前端源码和测试固定后全量64文件831项通过、0失败/待定、250.66秒，`.tmp/goal-g472-ui-all.json`/log，已重读JSON核对。TypeScript退出0，`.tmp/goal-g472-ui-types-final.log`；隔离Vite退出0、6.87秒，仅输出新路径`.tmp/goal-g472-ui-dist`，`.tmp/goal-g472-ui-build.log`，主chunk570.09kB，保留体积提示不调阈值。全部本批进程终态，限定diff检查通过。
- 实际浏览器验收已异步询问隔离端口/临时数据授权，尚未收到用户答复；工具接受问题不算授权，没有启动浏览器或截图。fake-indexeddb是内存实现，组件销毁/重挂载不是实际页面重载/磁盘持久化或真实Go/模型联合。Go及SDK源码本批未改未重跑，Go行为继续禁止重试/改名/换路径或更改策略；schema64未动，未操作用户数据库。未push/部署、测试机/8860/8880/用户作品/网关/凭据；原四流程、完整GAP/W0至W9和真实门禁仍保留，Goal保持active。

### G4.71 Skill包安装升级的事务回执与前端原请求恢复（2026-09-10）

- 上一轮用户询问工期，只作状态核对，属于no progress。本轮按active Goal继续实现，没有建立新目标或缩小到Skill管理；原四流程、能力范围、三执行模式和真实联合验收保持完整。没有再承诺未经核定的交期。
- 新公共ExecuteSkillPackageCommand覆盖install_zip、upgrade_zip、adopt_directory和update_directory，复用原prepareSkillZIP/Directory及persistSkillPackage，不另建安装引擎、数据库表或SDK模型循环。ZIP限制20MiB并按原始字节计算SHA256；目录只从可见注册目录读取，复制后比对确切名称/能力/版本/内容哈希，不接受客户端主机路径。对话作者草稿原有审批/事务回执不变。
- 请求哈希绑定当前工作区/用户、目标范围、操作、用户观察快照、源版本/哈希和ZIP原字节哈希。回执同安装事件在事务内提交，恢复时校验确切版本/事件/身份/范围及request_id/request_hash/source_hash。先取已完成回执再检查可变目录/安装状态，不重放后来的升级/卸载/重新安装；重试读取当前安装，不能把原安装成功解释为该版本仍启用。
- 升级事务内核对活动版本/状态/启停/事件数，事件数拒绝状态先变后恢复的旧确认；可信内部升级/目录更新也增加事务内已读快照检查。公共新安装不允许覆盖已有安装，要求选择该记录并明确升级。安装状态UPDATE必须影响一行。已完成诊断不因提交后读取失败被改写成failed。普通回执恢复不新增校验attempt；已并发开始校验的请求可能完成自己的诊断，但不重复安装事件。
- 四个HTTP包写接口现在要求UUID原键并返回data.receipt和data.installation；ZIP升级通过有界、严格JSON的X-Skill-Expected提交快照，目录更新的原active字段必须与完整expected一致。升级目标仍由已存安装决定，不能用query改变归属。options增加package_commands，前端对旧服务禁用不能保证协议的写操作。中文文件名使用RFC5987 Content-Disposition，原ZIP字节仍作为body上传。
- 前端将四种包操作并入已有pending写锁：保留原File/参数/快照/键，独立核对实际原文件哈希、回执和安装事件；错误回执保持未确认，不误报成功。只读刷新不换原命令，成功回执后的刷新失败只重读、不重装。仍沿用当前用户/工作区/角色变化后的迟到响应隔离，未增加整页持久化；beforeunload仅提醒，不算恢复实现。
- 首轮专项2文件80项通过、22.15秒，`.tmp/goal-g471-skill-ui-initial.json`/log。扩展四动作原请求恢复、14类错误回执、已提交但刷新失败和原字节校验后，3文件101项通过、27.85秒，`.tmp/goal-g471-skill-ui-expanded.json`/log。之后补API原键/中文名/目录快照传输用例，最后固定源码全量62文件790项通过、0失败/待定、221.51秒，`.tmp/goal-g471-ui-all.json`/log，JSON已重新读取核对；全量期间前端生产和测试源码未改。
- 最后TypeScript退出0，`.tmp/goal-g471-ui-types-final.log`；Vite native构建退出0、3.05秒，仅新建`.tmp/goal-g471-ui-dist`，`.tmp/goal-g471-ui-build.log`，保留562.38kB主chunk提示。Go最终全后端build退出0，`.tmp/goal-g471-build-final.log`；最后七包vet退出0，`.tmp/goal-g471-vet-complete.log`。中间新增HTTP测试缺any到map类型断言导致vet失败，`.tmp/goal-g471-vet-expanded.log`，已修正后重跑。Go新增当前权限、源删除、旧键/旧快照、回执损坏、重启、回滚及诊断保护用例仅源码，全部未运行，不算行为通过。
- 本批所有命令均已终态；SDK源码和固定依赖未改未重跑，schema未变，没有修改用户数据。未push/部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI、改变系统策略或启动浏览器。Goal仍active；下一项是整页原请求恢复及原范围后续复核，不签署最终review完成。

### G4.70 Skill生命周期的权威命令与原回执恢复（2026-09-10）

- 上一轮G4.69有代码及全量回归，为progress。本轮按自动Goal继续原完整范围，没有接续已回答的工期问题，也没有把目标缩为当前三个管理入口。
- 新`ControlSkillInstallation`统一用户启停、版本选择和卸载命令，HTTP要求UUID键及完整expected快照，并公布`lifecycle_commands`能力位。用户看到的active_version_id/status/enabled/event_count进入规范化请求哈希；工作区、安装及真实actor限定幂等域，HTTP不接收伪造actor。旧空body写协议现在拒绝，旧后端不提供能力位时新前端禁用这些操作，ZIP入口未借此假报已保护。
- 命令写入前和回执返回前复查当前用户/成员/范围；事务内检查已加载快照与真实当前状态，事件数阻止状态改走又改回后的旧确认。正常命令先在事务中建立幂等记录，再进行现有受管理快照交换；安装变更、审计事件和回执一起提交。事件绑定request_id及请求hash，重读必须与实际版本/事件/actor匹配。失败沿用现有交换回滚和启动恢复，不宣称已验收跨进程崩溃原子性。
- 原回执读取发生在现状态/包可执行性检查之前；旧启停、回滚或卸载已提交后，即使后来卸载、重新安装或换版本，也只返回原审计回执和新读取的当前安装，不重做旧命令。内部既有生命周期调用也补事务内加载快照校验；SDK作者安装与原生运行循环没有改写。
- 管理页冻结原安装对象/目标版本/请求键，传输路径转义，校验回执请求、安装、范围、包版本/hash、actor及绑定审计事件。未知响应和错误回执保留原操作，其他写操作与目标切换锁定，只读刷新不改原参数。重试后显示原操作已完成与后续状态变化，而不是将历史receipt当作当前可用状态。角色变化/迟到结果保护保留；未知后再撤权的403/404不被当成原操作未执行。离页有beforeunload保护，但**没有整页重载持久恢复**。
- Go测试源码新增三范围四操作的后续状态/重试、旧快照及ABA、事务内变化/撤权、缓存权限、损坏或重绑定回执、重开Store、回执失败时数据库与文件回滚、同键/异键竞争；HTTP源码覆盖必填键/快照、当前权限及原回执。已有公开生命周期用例更新到新契约。全部Go行为仍未运行，没有执行或规避受限测试程序。
- 首轮前端专项2文件62项通过、12.53秒，`.tmp/goal-g470-skill-ui-initial.json`/log；扩大后78项通过、15.02秒，`.tmp/goal-g470-skill-ui-expanded.json`/log。初轮全量767项通过、211.58秒，`.tmp/goal-g470-ui-all.json`/log，但运行期间继续补校验和用例，不当作最终同版证据。源码固定后最终全量61文件768项通过、0失败/待定、204.62秒，`.tmp/goal-g470-ui-all-final.json`/log，已重新读取JSON核定。
- 最后TypeScript退出0，`.tmp/goal-g470-ui-types-complete.log`；隔离Vite退出0、2.55秒，`.tmp/goal-g470-ui-build.log`，产物仅`.tmp/goal-g470-ui-dist`，保留558.18kB主chunk提示。Go全后端build退出0，`.tmp/goal-g470-build.log`；最后七包vet退出0，`.tmp/goal-g470-vet-final.log`。所有本批进程已终态。SDK与固定依赖未变未重跑，schema未变，未改用户数据库。
- 下一组明确代码缺口是旧ZIP安装/升级及目录纳管/更新：目前没有本批的用户原快照/持久回执闭环；目录更新的expectedActiveVersion检查仍在persist事务之外，不能覆盖另一Store推进版本的竞争。后续继续补这组及完整浏览器恢复，不以本批替代Skill作者/执行/资源/依赖的全部生命周期。原四流程、W0至W9/GAP和真实环境门禁保留；未push/部署、访问测试机或8860/8880、改用户作品/网关/凭据、安装OCI或改策略。Goal仍active，非最终review通过。

### G4.69 Skill版本切换状态与管理页操作边界（2026-09-10）

- 用户问“离review完还有多久”，先核对当前Goal和记录；仍有代码缺口及独立外部验收条件，没有编造剩余小时或完成比例。按原授权继续开发，不创建新目标、不缩小范围。
- `ActivateSkillVersion`此前用执行模式是否受支持直接决定enabled，导致已禁用Skill仅选择历史版本就被重新启用。现在分别处理执行模式与用户启停状态：受支持版本保留安装原enabled，更新受管理快照并同步注册表的同一禁用状态；仍不能执行的模式维持pending。Runtime生命周期测试新增禁用时连续换两个版本、重启保留状态及显式启用；HTTP测试新增相同公开入口断言。**这些Go行为用例未运行**，不以build/vet代替。
- `SkillManagement`增加同步运行锁，避免同一事件批次重复写入和竞争选中；加载失败后旧状态不再允许写操作，提供独立只读刷新，不重新扫描或重发此前变更。变更响应成功但刷新失败会单独提示“已提交、未读取最新状态”。角色/项目上下文改变和页面卸载后忽略旧操作结果，App按用户与工作区分开管理页实例。
- ZIP文件选择绑定打开时的安装范围或所选安装/版本/启停/状态，发生变化必须重新选择，不把文件静默上传到新目标。目录更新预览核对安装ID、capability和当前版本，错配时不展示应用入口。上述是前端本地观察保护，**没有实现远端事务CAS，也没有给旧写API新增持久回执**。
- 首轮管理页专项23通过、9.53秒，`.tmp/goal-g469-skill-ui.json`/log；补充正常升级、失败后反复只读刷新及卸载页面晚错误后，最后前端全量61文件750通过、0失败/待定、212.55秒，`.tmp/goal-g469-ui-all.json`/log，已重新读取JSON核定。最后TypeScript退出0，`.tmp/goal-g469-ui-types-final.log`；隔离Vite退出0、2.10秒，`.tmp/goal-g469-ui-build.log`，仅输出`.tmp/goal-g469-ui-dist`，保留554.49kB主chunk提示。
- 全后端build退出0，`.tmp/goal-g469-build.log`；七包vet退出0，`.tmp/goal-g469-vet.log`。SDK和固定依赖未改未重跑，schema未变；所有本批命令已取得终态。未运行受限Go测试，未push、部署、访问测试机/8860/8880或改用户作品/数据库/网关/凭据，未安装OCI、改安全策略或启动浏览器。
- 下一项仍是旧管理写API的权威事务与原请求恢复：ZIP升级只接收安装ID和包，版本选择/启停/卸载未携带用户观察快照；未知响应若再次发新命令可能重复审计或覆盖远端后续状态。目录更新已有当前版本和内容hash检查，不能把它当作整个生命周期已幂等。原四流程（novel_to_script、non_novel_to_script、video_reference_creation、script_continuation）及全部能力/真实验收继续保留，Goal active，非最终review完成。

### G4.68 Skill安装恢复、升级与实际选择入口 review（2026-09-09至10）

- 上一轮有代码与同版回归，为progress；本轮继续原Goal的Skill用户闭环，不接续已经回答过的工期问题。只修改前端源码/测试及记录；后端、SDK、依赖固定版本和数据库schema均未改。
- 只读核定`project_skill_draft.go`的快照hash、ZIP校验、显式用户/SDK审批安装、事务内身份/范围/CAS检查、持久回执与当前执行目录扩展；`skill_selection.go`的多范围优先级和冻结目录；`project_skills.py`的安装回执、当前inline装载、依赖失败可再次加载及任务主版本固定。现有Go对应行为测试源码覆盖安装/升级/重启/并发/撤权，未执行，不能视为真实后端验收。
- 工作文件窗口现在按需首次挂载，关闭或Escape只隐藏同一窗口，保留未确认安装；文件事件/手动刷新不再通过key重建安装面板。未知结果锁住范围、返回文件、重新校验及侧边文件选择，仅原参数/原键可重试。不同作品/身份仍隔离，未宣称跨整页刷新或退出会话持久恢复。
- 校验结果核对作品/根目录及安装目标；安装回执核对当前用户工作区、目标scope/ref、确切安装ID、capability/name和包version/hash/mode。回执错配不显示成功且保留原操作。目录在检查后变更时，新安装必须显式重新校验；已发出且结果未知的原安装可继续查回执。权限降为viewer后不保留安装入口，迟到回执不触发成功回调。
- 区分安装已落库、当前启用版本、禁用/不可用状态；旧版本回执不冒充当前版本可调用。已安装但能力目录刷新失败提供独立重读按钮，不重新安装。真实App发现外层`load`会吞掉该失败，现仅安装回调启用严格刷新结果；普通后台刷新仍保持原处理。完整App夹具验证成功/刷新失败两条路径进入Skill菜单并发送正确版本，不把此用例称为真实SDK/Go/浏览器联合。
- Composer新请求提交前核对当前目录中所选Skill的版本、scope/hash和可用性，过期选择需显式重选；选择同ID新版不会误当成取消。增加通用Agent选项，已选Skill被移除后仍可保留原文切回通用对话。对于此前结果未知且内容/引用未变的发送，仍允许原Skill版本/原键重试，不将恢复变成新一轮。
- 工作文件列表及内容读取补当前作品、路径、版本和分页回执检查，错误内容不用于下载或Skill校验入口。新增测试使用完整安装回执，不继续使用只有enabled和registry_status的弱夹具。样式沿用现有工作文件窗口与图标，没有启动浏览器或宣称视觉验收。
- 初轮2文件44项通过、12.67秒，`.tmp/goal-g468-skill-ui-initial.json`/log；扩大到实际工作台、选择器等4文件197项通过、55.14秒，`.tmp/goal-g468-skill-ui-expanded.json`/log。之后增加原未确认消息保持原Skill版本的回归。最后同版前端61文件738项通过、0失败/待定、197.97秒，`.tmp/goal-g468-ui-all.json`/log，已重读JSON核对。全量期间前端生产/测试源码未改。
- 最后TypeScript退出0，`.tmp/goal-g468-ui-types-complete.log`；隔离Vite退出0、2.11秒，`.tmp/goal-g468-ui-build.log`，仅输出新建`.tmp/goal-g468-ui-dist`，保留552.41kB主chunk提示。Go全后端build及七包vet退出0，`.tmp/goal-g468-build.log`、`.tmp/goal-g468-vet.log`，仍不是Go行为通过。
- SDK预检区分环境：项目`.venv`没有SDK分发包，系统Python有固定SDK但无全局pytest；现有项目`.deps`下pytest在沙箱内读取被拒绝。预检失败时没有启动SDK用例、未安装替代包或改权限。经工具正常读取/执行审批，核定`openai-agents=0.21.1`、`openai=3.3.1`、`pytest=8.4.2`，证据`.tmp/goal-g468-sdk-runtime-approved.log`；在经核定不存在且位于工作区内的新目录完成SDK全量1507项通过、0失败/错误/跳过、309.04秒，`.tmp/goal-g468-sdk-all.xml`/log，已重读XML核对。SDK和前端全量期间各自源码未改变，本批全部进程已终态，限定diff检查通过。没有重试Go测试、更名/换路径绕过Application Control或修改系统策略。
- 下一项源码证据：`SkillManagement.tsx:perform`缺同步提交锁，ZIP升级API未传用户看到的当前版本，版本激活/启停/卸载还需核对旧确认与远端变更及未知响应重试；未用本批草稿安装修复替代管理页完整生命周期。原四流程、W0至W9/GAP和真实Go/OCI/网关压缩/浏览器及回滚验收继续保留。未push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据，Goal仍active，不签署最终review。

### G4.67 附件选择、批次编辑与运行输入 review（2026-09-09）

- 用户询问距离review完成多久；核对当前Goal、进度和源码后，没有提供未经核定的小时承诺。仍在修复review发现的实际代码缺口，不是只剩签字；原四流程、Skill用户闭环、完整能力清单与真实环境验收均保留，未缩为当前附件专项。
- 视频批次前端接入现有移除、排除/纳入、重新收集接口，复用期望版本、原键重试和权威读取。已被Run输入使用的封存版本仍由后端拒绝重新收集，没有放宽历史输入约束。批次回调改为等待父组件完成附件同步，已成功但父组件失败时保留回执，只重读、不重新写。未知操作关闭弹窗后仍可重新进入，父组件发送/上传/Skill切换继续锁定。
- 新增共用附件引用处理：移除ZIP时移除子文件；单独移除子文件时解除整个ZIP引用并保留可见独立兄弟文件；排除成员保留其引用以便重新纳入。远端新增已纳入成员按当前作品、可用视频及当前快照补取，不借用其他作品材料。视频发送仅带已纳入成员且按批次排序；通用模式附带其他文档时保留这些材料并不传纯视频批次自动绑定，避免混合ZIP内容静默丢失。
- 新建作品上传按文件记录成功结果，后续失败不会忘记此前已上传的附件，可移除成功附件后重试剩余文件。Composer也逐项保留已成功引用。切换到其他Skill后移除视频会解除旧批次，返回视频Skill从剩余附件建立批次，避免被运行输入重新带入已移除成员。准备批次结果未知时保留原参数及请求键，冻结其他材料动作，提供原操作重试。
- 批次行新增复选框和移除图标，窄屏动作换行、文件名保留可收缩宽度，长缺集文本可换行；沿用既有样式和Lucide图标。没有开启浏览器、截图或宣称视觉验收完成。旧缓存首条消息和更多跨模式材料组合继续纳入后续原流程复查，不将本批用例表述为完整浏览器/模型闭环。
- `getAssetSetSnapshotTx`在同时给定集合ID和版本ID时核对二者配对，仍支持合法按版本读取历史；运行输入对不存在/错配版本返回领域错误。新增Go源码覆盖错误配对、合法历史读取和运行输入拒绝；未执行这些行为用例，没有修改用户数据库或增加schema。
- 首轮附件专项5文件79项通过、22.28秒，`.tmp/goal-g467-materials-initial.json`/log。首轮全量61文件700通过/1失败、207.39秒，`.tmp/goal-g467-ui-all.json`/log；失败是新增发送用例未等待弹窗关闭后的发送门禁解除，修正为检查按钮实际可用后点击，不延长固定等待或弱化输入断言。补混合ZIP文档保留用例后，专项5文件81项通过、19.94秒，`.tmp/goal-g467-materials-final.json`/log。
- Go build退出0，`.tmp/goal-g467-build.log`。首轮vet发现新增测试漏用Store接收者，`.tmp/goal-g467-vet.log`；修正后七包vet退出0，`.tmp/goal-g467-vet-final.log`，不算Go行为通过。最终TypeScript退出0，`.tmp/goal-g467-ui-types-complete.log`。首次Vite未使用仓库已有native配置加载参数，配置预打包进程spawn EPERM，`.tmp/goal-g467-ui-build.log`；按原有native方式构建通过，末版日志`.tmp/goal-g467-ui-build-complete.log`，产物仅在新建的`.tmp/goal-g467-ui-complete-dist`，未修改系统策略或原服务。
- 最后同版前端全量61文件703项通过、0失败/待定、209.29秒，`.tmp/goal-g467-ui-all-final.json`/log，已重读JSON核对。末版Vite构建1.65秒、主chunk547.87kB，保留原体积提示，没有调阈值。最后全量期间前端生产与测试源码未改，本批所有测试/构建进程均已终态，限定diff检查通过。SDK及固定依赖未修改未重跑；Go测试禁令、真实OCI/网关原生压缩和用户页面联合验收限制不变。未push/部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI或绕过系统限制。Goal仍active，不签署最终review通过。

### G4.66 历史最终稿独立保留与候选切换 review（2026-09-09）

- 用户继续询问距review结束多久；核对active Goal及当前剩余清单后，没有再提供未经核定的小时承诺。继续原完整范围，未创建新目标或改为仅交付本批。
- `createScriptCandidateTx`改为按确切聚合版本重读既有候选；新聚合创建新的candidate_id并记录前一候选引用，不更新原候选的正文版本、状态或时间。原最终稿继续有效，直到用户显式确认替换；历史候选依然可选择/导出，同Run不会被去重成一份。已取代版本的原候选可重读，但不能为没有候选的已取代版本新建候选。
- schema64移除每Run唯一约束，保留聚合版本唯一及作品唯一最终稿约束，新增Run索引。迁移按索引元数据识别旧约束，在专用连接及事务内重建，保留原行、命名索引/触发器及候选自引用、定稿、预览、导出外键；检查引用后提交，并恢复/核定外键检查。沿用Open迁移前备份及最后成功标记机制。**没有执行迁移或修写用户数据库**；也不能据此恢复旧代码此前已覆盖但缺乏原始引用证据的最终稿。
- 完成后编辑仍复用原Run的受控编辑/交接/审核流程，但不再将全部已确认正文及交接退回pending_approval并清除confirmed_at。仅新正文与相应刷新交接进入审批，原已确认单集被现有下游完整输入收集器继续纳入审核/聚合。没有放宽审批版本集合、当前Task绑定或写锁门禁，也不声称原Run本身变成不可变历史快照。
- 新增Go测试源码覆盖同Run连续三个候选、确切版本重读、旧最终稿保留/替换/重选、schema63迁移引用与备份、约束保留及损坏引用导致回滚/恢复外键检查；扩展原12集完整生命周期，在编辑中、刷新后和再次聚合后检查旧正文/确认时间及新旧导出，检查本次只审批一对正文与交接。全部未执行，不计Go行为通过。
- 新完整App用例首轮暴露真实前端缺陷：菜单已选择旧候选，但ArtifactWorkspace的普通刷新单向版本保护仍保留新版。显式候选导航现按所选版本区分工作区实例；普通刷新仍保持同一实例，不撤销旧响应保护，切换前原脏稿确认保留。前端首轮2文件138通过/1失败、59.07秒，`.tmp/goal-g466-candidate-ui.json`/log；修复后相同2文件139通过、0失败/待定、43.75秒，`.tmp/goal-g466-candidate-ui-final.json`/log。没有把真实失败改成弱化断言或只延长等待。
- 最后TypeScript退出0，`.tmp/goal-g466-ui-types-final.log`；隔离Vite退出0、3.93秒，仅输出`.tmp/goal-g466-ui-dist`，`.tmp/goal-g466-ui-build.log`，保留541.57kB主chunk提示。全后端build退出0，`.tmp/goal-g466-build.log`；七包vet退出0，`.tmp/goal-g466-vet.log`。最后前端同版全量60文件674项通过、0失败/待定、198.40秒，`.tmp/goal-g466-ui-all.json`/log，已重新读取JSON核对。全量运行期间生产及测试源码未变，本批所有进程均取得终态，限定diff检查通过。
- SDK及固定依赖未改未重跑；Go测试仍被Application Control限制，未重试/更名/换路径或修改策略。未push/部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI或启动浏览器。原四流程、附件完整生命周期、Skill用户闭环、完整能力范围核对及真实环境门禁继续保留，Goal仍active，不签署最终review完成。

### G4.65 素材批次确认恢复与候选版本操作 review（2026-09-09）

- 上一轮为progress，本轮继续原Goal，没有重复估时或改成阶段性验收目标。固定写入口的代码补接与真实环境门禁继续分开记录。
- 素材批次创建、变更、封存、重新收集在事务内复查当前用户、成员、作品及scope后读取幂等结果；读取当前/历史批次也复查项目访问。缓存核对确切集合、版本、状态和持久成员/版本内容；后续版本推进后仍可返回原提交回执，不把旧回执重新执行。来源消息必须属于作品。现有HTTP body hash与请求格式保留，没有新增数据库schema。
- 修复自动排序时排除成员空集号的解引用、全部成员排除后仍可封存，以及缺集按不受限最大集号逐项枚举的问题。集号明确限制为1至10000；超范围文件名不自动认作有效集号，人工请求明确拒绝，历史异常成员在变更/封存时也校验。更新当前集合必须影响恰好一行，否则事务回滚版本、成员、事件及回执。重新收集按结构化JSON中的精确asset_set_version_id判断下游引用，不因说明文字包含ID而误拦。
- 前端批次弹窗补同步提交锁、viewer限制、作品/集合/版本与回执检查。结果未知保留原操作，关闭再打开同一页面弹窗仍能重试原键；已收到成功回执但权威读取失败时只重读，不再提交修改。读取当前结果后才更新父视图，后续已重新收集的批次不会被旧sealed回执覆盖。未提交集号遇远端版本变化时保留并要求显式刷新，确认其中一项也不清空其他未提交集号。键只在当前页面生命周期保留，不声称跨浏览器刷新/页面导航持久化。
- 确认新建作品与工作台的“继续上传”各有遗漏：前者忽略旧批次外的新增素材，后者重新创建批次。现向仍收集中的原批次追加尚未加入的资产，保留先前成员，创建/追加使用分开的原请求键；补两处真实组件入口用例和传输检查。完整附件删除、输入快照与运行启动仍纳入原四流程联合复核，不以分次上传用例概括整个材料生命周期。
- 候选菜单记录用户实际打开的版本；同一候选被远端推进后保留旧预览，明确阻止直接导出/定稿，用户重新选择且未拒绝脏稿确认后才解除。对照原有candidate_test.go生命周期用例发现G4.64状态门禁误拦historical_final重选，已恢复历史最终稿合法重选/导出资格，仍检查当前权限、归属及确认/已取代版本；前端补覆盖，未运行Go用例。
- 前端初始专项6文件99项通过、25.43秒，`.tmp/goal-g465-ui-focused.json`/log；扩大工作台专项7文件222项通过、66.04秒，`.tmp/goal-g465-ui-expanded.json`/log；加入继续上传与原键传输后6文件104项通过，`.tmp/goal-g465-ui-batch-final.json`/log。第一次全量60文件672项通过、177.63秒，`.tmp/goal-g465-ui-all.json`/log；恢复历史最终稿资格后最后同版全量60文件673项通过、0失败/待定、195.96秒，`.tmp/goal-g465-ui-all-final.json`/log，已重读JSON核对。
- 最后TypeScript退出0，`.tmp/goal-g465-ui-types-verified.log`；隔离Vite退出0、2.07秒，仅输出`.tmp/goal-g465-ui-dist-final`，`.tmp/goal-g465-ui-build-final.log`，保留541.49kB主chunk提示。全后端build退出0，`.tmp/goal-g465-build-verified.log`；七包vet退出0，`.tmp/goal-g465-vet-verified.log`。新增Go测试源码覆盖四命令fresh/cached当前授权、历史回执、跨工作区读取、排除空集号/空封存、超范围集号、CAS故障回滚和精确引用；均未运行，不计Go行为通过。所有本批进程均取得终态，限定diff检查通过。
- **下一项明确保留**：`createScriptCandidateTx`仍按source_run_id更新现有候选，store schema仍有每Run唯一候选约束；`artifact.go`完成后编辑会重新打开原Run并调整当前剧本版本状态，随后`runtime_step_executor.go`再次聚合更新候选。因此原最终稿在编辑期间和再聚合后能否独立保持可读/可导出仍是代码缺口，不能用前端显式版本检查宣称已解决。下一步须覆盖编辑期间、生成新版、替换及重新选择旧最终稿，而不是只保留候选名称。
- 未push、部署、操作测试机/8860/8880/用户作品或数据库/网关/凭据、安装OCI、修改系统策略或启动浏览器；SDK未改未重跑。原完整范围和外部验收门禁保留，Goal保持active，不签署最终全局review完成。

### G4.64 候选稿版本、定稿预览及导出回执 review（2026-09-09）

- 用户询问距代码review结束多久；核对active Goal、当前源码与剩余清单，继续原范围，不另报未经核定的小时数。代码仍有待修项，不能把所有未完成归因于外部验收限制。
- 候选预览、最终确认及导出在事务内复查当前用户/成员/作品与scope后读取幂等记录；预览携可选期望合集版本，确认时重新核对候选实际版本形成的预览hash。候选同一source Run推进合集版本后，旧预览不能确认新正文。历史成功确认回执绑定持久selection、已消费preview和已解决approval，返回原提交记录而不是重新设为最终稿。
- 导出读取与命令处于同一事务，原键命中先于可变候选当前版本门禁，保留已生成历史文件回执。缓存绑定作品、候选、合集版本、格式、持久文件元数据和有效期；单集必须是已确认/已取代版本且集号匹配。提交结果不确定时不删除可能已被持久回执引用的文件；这不是所有失败无孤立文件的保证。
- 前端改为显式选择候选，校验作品/Run/合集版本后经既有脏稿确认进入精确版本预览；不再挂载时自动覆盖正文。合集阅读器原先读取各集current_version_id，现读取所展示合集保存的精确unit_refs，校验成员、状态、集号和重复引用，迟到响应不覆盖新选择。补完整App验证历史正文、焦点刷新、对话版本引用和拒绝丢弃草稿。
- 最终稿预览和导出手动重试使用原请求键，检查回执目标；下载走固定同源API并核对实际字节长度与SHA-256，不跟随回执URL。结果未知时阻止竞争操作，失败留在当前菜单，刷新失败不丢原命令。确认接口类型更正为实际嵌套FinalSelectionResult，工作台核对selection/candidate/approval后重新加载权威状态。页面内请求键不声称跨重载持久化。
- 新增Go用例源码覆盖当前授权fresh/cached、候选推进后旧预览、历史确认与导出回执、文件过期/格式错绑和单集状态/集号拒绝；Application Control限制下全部未运行，不计行为通过。最终全后端build退出0，`.tmp/goal-g464-build-final.log`；七包vet退出0，`.tmp/goal-g464-vet.log`。
- 首次扩大专项183通过/1失败，`.tmp/goal-g464-candidate-expanded.json`/log；失败是测试误用预览按钮名称定位最终确认按钮。按实际“确认设为最终稿”修正测试定位后，5文件184项通过、0失败/待定、40.90秒，`.tmp/goal-g464-candidate-verified.json`/log。第一轮全量640通过/6失败、184.08秒，`.tmp/goal-g464-ui-all.json`/log：新增历史候选用例未等待异步提交完成便检查调用，后续同作品测试也受到待发送记录影响。按照本文件已有原生摘要测试的等待模式，明确等待sendMessage被调用，不改变业务逻辑或放宽断言；随后同版全量58文件646项通过、0失败/待定、193.93秒，`.tmp/goal-g464-ui-all-final.json`/log，原六项均通过，已重读JSON核对。
- 最后TypeScript退出0，`.tmp/goal-g464-ui-types-final.log`；隔离Vite退出0、9.64秒，产物仅`.tmp/goal-g464-ui-dist`，`.tmp/goal-g464-ui-build.log`，保留536.34kB主chunk提示。Vite后仅修正测试等待与文档，生产源码未变。所有本批测试/构建进程均已取得终态，限定diff检查通过；夹具回归不替代浏览器或真实Go/模型联合验收。
- 仍须核对：用户已打开旧候选后，远端将同一候选推进新版时，当前历史预览与后续导出/定稿操作是否始终绑定同一版本。素材批次仅只读检查，尚未修复当前授权/命令回执和界面重试保护；另发现全部成员排除仍可封存、超大集号导致逐号缺集枚举等问题。保留原用户完整流程和外部门禁，不将本批报告称为最终独立review完成。
- 未push、部署、操作测试机/8860/8880/用户作品或数据库/网关/凭据、安装OCI、修改系统策略或启动浏览器；SDK未改未重跑。Goal保持active，本批为进度，不签署完成。

### G4.63 返修目标、执行入队及遗留尝试 review（2026-09-09）

- 用户再次询问距离review完成多久。本轮核对active Goal、当前源码与固定清单，未给未经核定的小时承诺。保持原范围及本地隔离边界，不把代码检查结束等同真实验收通过。
- 目标确认补事务内当前用户/成员/项目授权、scope、请求版本及候选产物/base归属；原幂等记录现在实际写入和读取，缓存绑定确切请求、目标和期望版本。取消后不能选择目标，旧选择回执可返回已推进的当前请求，不再次触发生成。
- 公开执行接口改为提交持久入队命令并返回202，由现有返修Worker处理；首次确认/显式失败重试使用版本检查，原键查询即使执行后来失败也不会重新入队。目标确认接口同样只提交目标/排队状态。没有增加另一套队列、数据库schema或模型循环，保留原提交/审批入口的既有自动返修逻辑。
- Sidecar读取后携期望版本领取尝试，防止旧读取在别的尝试失败后又发起生成；失败回调只影响当前仍运行的确切尝试，取消上下文后用有界清理上下文记录失败。Worker核对超过HTTP超时加30秒的遗留尝试，标记失败等待显式重试，不自动重放模型调用。
- 复查实际SDK内部提交路径发现本轮初版领取授权过严，会误拦截合法Agent执行，已修正并补测试源码：用户目标确认/重试仍拒绝AgentActivity，执行已确认返修则重新解析当前执行主体和成员权限。保留服务身份与委托用户的区分以及SDK提交中的既有AllowTerminal边界，不把内部执行权限当作用户确认权限。
- Workbench目标/执行携显式原请求键和期望版本，检查当前目标、回执请求/作品/会话/消息/产物/base和递增版本；成功/失败都刷新权威projection。共享返修提交锁和待确认操作记录，切换对话/进度后不能改选另一候选或提交相反动作；原操作可重试，确定性拒绝释放。卡片核对定位结果归属，错绑时禁用选择但保留合法取消；viewer不可写。键与待确认记录仅限当前页面生命周期，不声称跨浏览器重载持久化。
- 前端专项3文件138项通过、60.02秒，`.tmp/goal-g463-revision-initial.json`/log；同版全量56文件617项通过、0失败/待定，250.86秒，`.tmp/goal-g463-ui-all.json`/log，已重读JSON核对。覆盖未知响应原键重试、错误回执拒绝、跨视图相反操作、运行中权威刷新和只读定位卡，属于本地夹具，不是浏览器或真实Go/模型联合。
- TypeScript退出0，`.tmp/goal-g463-ui-types-final.log`；隔离Vite退出0、1.50秒，产物仅`.tmp/goal-g463-ui-dist`，`.tmp/goal-g463-ui-build.log`，保留530.81kB主chunk提示。Go全后端build退出0，`.tmp/goal-g463-build-final.log`；七包vet退出0，`.tmp/goal-g463-vet-final.log`。新增Go测试源码含当前授权fresh/cached、目标选择回执、事务回滚、显式恢复/旧回调、SDK委托执行及公开202入队不调用适配器，均未运行，不计行为通过。
- 本批所有构建/测试进程均已终止，diff check通过。SDK源码未改未重跑，未push/部署/操作测试机/8860/8880/用户作品/数据库/网关/凭据，未安装OCI、修改策略或启动浏览器。下一项按同一清单核对候选导出/定稿预览及素材批次确认；原完整用户流程和外部验收门禁保留。Goal保持active，本批是progress，不是最终review通过。

### G4.62 主对话控制与排队编辑 review（2026-09-09）

- 上轮G4.61为progress，本轮继续固定剩余入口，不新增产品方向。检查HTTP与Runtime当前权限规则后，保留同工作区编辑者可取消、只有消息作者可编辑/暂停/继续；没有把既有协作者停止权限静默删除。
- 取消主轮原先缺少事务内当前身份/成员/项目检查，现与排队编辑及暂停/继续共用授权函数。拒绝AgentActivity和显式无效/服务身份，核对当前项目/工作区及非空命令scope，保留无Principal内部调用约定和HTTP认证边界。取消仍按资源状态天然幂等，不另建缓存表或恢复状态机。
- 排队编辑与暂停/继续读取缓存前核定当前授权，缓存绑定确切turn、作品、工作区、作者和会话，再核对编辑正文或控制动作对应状态。返回当前权威turn，保留消息已开始或后来继续后的原键查询，不将旧缓存状态回写执行。首次编辑仍要求accepted、未开始、无SDK checkpoint及原正文CAS，附件和Skill快照不变；HTTP请求hash、缓存格式、SDK协议与数据库schema均未改。
- Workbench统一核定当前控制目标、权限、状态、同步锁和人工重试键，验证回执turn/作品/工作区/会话/作者；成功或失败都刷新projection，不直接以旧控制回执替换当前消息和执行行。前端取消/编辑传输接可选显式键并转义ID，正文合同不变。键限当前页面生命周期，不宣称跨重载持久化。
- 主轮卡加入同步防竞争、未知结果原操作重试和只读门禁；不更换turn身份的状态更新保留本地草稿及追加组件，迟到错误不污染新的执行状态。排队编辑未知时冻结原正文，明确拒绝后可修正；执行终止时仍可查看/放弃尚未确认的草稿。主轮卡移到视图切换条件之外并按当前视图隐藏，避免切到进度再返回时清空草稿/丢失待确认操作；没有创建第二份控制卡或更改视觉样式，没有浏览器截图验收。
- 前端初轮3文件123项通过、43.28秒，`.tmp/goal-g462-turn-initial.json`/log；扩大组件/App/传输及跨视图、只读/协作者、错误回执和响应丢失后权威刷新后141项通过、52.68秒，`.tmp/goal-g462-turn-expanded.json`/log。最终同版前端56文件601项通过、0失败/待定，254.70秒，`.tmp/goal-g462-ui-all.json`/log，已重新读取JSON核对。它们是本地夹具，不代表真实模型/Go/浏览器联合通过。
- TypeScript退出0，`.tmp/goal-g462-ui-types.log`；隔离Vite退出0、2.46秒，仅输出`.tmp/goal-g462-ui-dist`，`.tmp/goal-g462-ui-build.log`，保留526.39kB主chunk提示。后端全量build退出0，`.tmp/goal-g462-build.log`；新增测试源码后六包vet退出0，`.tmp/goal-g462-vet.log`。新增Go用例覆盖四操作fresh/repeated撤权矩阵、协作者权限、状态不变及跨主体/scope/动作缓存拒绝，未运行Go测试，不计行为通过。
- 本批所有测试/构建命令均取得终态。SDK源码未改未重跑，未push/部署/操作测试机/8860/8880/用户作品/数据库/网关/凭据，未安装OCI、修改系统策略或启动浏览器。下一步继续同一清单的返修目标/执行，原完整用户流程与外部验收门禁保留；Goal保持active，本批是progress，不是最终review通过。

### G4.61 工具审批与私有规则确认 review（2026-09-09）

- 用户追问距review完成还要多久。本轮核对当前Goal和清单后明确不再给出无依据的小时数，也不把代码审查完成等同真实环境验收；原Goal保持active，继续授权范围，无新增产品方向。
- Runtime普通工具审批现在于同一事务内读取当前用户/工作区/成员/项目，核对审批与调用归属及命令scope后再写入或读幂等缓存。带AgentActivity或显式非有效用户身份不能自行审批；无Principal的现有内部调用约定保留，HTTP认证边界未放宽。规则专用的本人/工作区管理员校验仍先执行，未把普通编辑权限当作私有规则审批权限。
- 缓存回执绑定审批、调用、工作区、作品、会话、原subject hash、递增版本、决议状态与resolution动作。当前审批已处理、工具后来执行完成时仍可返回原合法回执，不重复执行。首次处理还要求动作在当前options内。没有修改原HTTP请求hash、缓存格式、数据库schema或三种原生恢复模式。
- Workbench按确切审批保留人工重试键、同步提交锁和待确认操作；未知结果期间切换对话/进度页也不能提交相反动作。回执归属不匹配时不消费审批，成功/失败均刷新projection；已知更高版本不被迟到pending projection覆盖。键和待确认状态限当前页面生命周期，未宣称跨重载持久化。
- 普通工具和私有规则卡加入同步防重复、viewer只读、身份/版本切换后的迟到错误隔离，以及未知结果仅重试原动作的保护。规则私有内容仍由匹配调用/作品/arguments hash且已授权的专用读接口返回，审批解决后隐藏；网络结果未知不清空合法提案，确定性拒绝后重新读取。切换页后被阻止的相反操作不应锁死原操作重试，已修正并增加完整App断言。
- 前端首轮4文件180项通过，56.59秒，`.tmp/goal-g461-approval-focused.json`/log；之后加入私有规则完整App批准/拒绝重试及跨视图原操作恢复断言。最终同版全量56文件583项通过、0失败/待定，325.34秒，`.tmp/goal-g461-ui-all.json`/log，已重新读取JSON核对。它们是App/组件/传输夹具，不是浏览器或真实Go/模型联合。
- 当前TypeScript退出0，`.tmp/goal-g461-ui-types-final.log`；隔离Vite退出0、2.47秒，仅输出`.tmp/goal-g461-ui-dist`，`.tmp/goal-g461-ui-build.log`，保留523.71kB主chunk提示。后端全量build退出0，`.tmp/goal-g461-build.log`。首次vet因新增测试辅助函数重名退出1，`.tmp/goal-g461-vet.log`；改为工具审批专用命名后六包vet退出0，`.tmp/goal-g461-vet-final.log`。新增Go测试源码覆盖fresh/cached撤权、服务身份、归属/options、合法工具完成后缓存重试及错误回执字段，均未运行，不计为行为通过。
- 本批所有测试/构建命令均取得终态。SDK源码未改未重跑，未push/部署/操作测试机/8860/8880/用户作品/数据库/网关/凭据，未安装OCI、修改系统策略或启动浏览器。下一步继续同一清单中的主轮终止/排队编辑，并保留原完整用户流程、回归和外部验收门禁；本批是progress，不是最终review通过。

### G4.60 后台控制、启动确认及只读入口 review（2026-09-09）

- 上轮G4.59为progress，本轮继续原后台任务和启动闭环。只修改前端代码/测试/类型和进度记录；后端StartRun/StartAgentTask现有事务内提案授权、原请求hash及消费回执绑定已只读核定，未重做这部分，也没有重跑或宣称通过Go/SDK行为。
- 后台取消/暂停/继续/重新执行API接显式请求键；完整工作台按确切任务同步防竞争，保留未知结果和COMMAND_IN_PROGRESS的原键，核对回执任务/项目/会话，成功或失败均刷新projection。回执不直接回退当前任务状态。键为当前页面生命周期，未宣称跨重载持久化。
- 后台卡和配置/启动卡使用当前Principal的viewer门禁。后台控制未知结果时仅允许原操作重试，任务/attempt变化后的迟到错误不进入新执行；追加输入组件保持挂载，暂停/继续及撤权不丢未提交草稿。只读仍能查看任务结果。
- 两类启动共用前端提交门禁，检查当前提案版本/hash/类型，同一确认防重复。请求失败也刷新消费状态，服务端已启动时恢复为已确认而不再次发起。后台回执核对提案ID、项目/会话/能力版本；Run回执核对项目/会话/能力版本和非空Run ID，原确切提案消费绑定仍由Runtime负责。晚到回执不覆盖较新的提案，旧projection不能把已确认的同版卡回退为pending。
- 配置回执增加原提案ID、递增版本、状态、能力版本及可见项目/会话字段校验；启动表单丢弃卸载后的迟到错误。类型声明补实际JSON字段，不修改服务端协议。
- 首次专项4文件165项中164通过/1失败，`.tmp/goal-g460-controls-initial.json`/log；旧动态Skill测试返回了novel_to_script配置，被新归属校验正确拒绝。修正该夹具及其他仅检查请求的旧不完整回执后，165项全部通过，42.69秒，`.tmp/goal-g460-controls-after.json`/log。包含四控制手动/自动重试、未知结果、外部消费恢复、错误目标回执、viewer完整App，以及草稿保留/同步双击/迟到错误组件用例。
- TypeScript退出0，`.tmp/goal-g460-ui-types-final.log`；隔离Vite退出0、1.74秒，仅输出`.tmp/goal-g460-ui-dist`，保留519.86kB主chunk提示。最终前端55文件560项通过、0失败/待定，271.00秒，`.tmp/goal-g460-ui-all.json`/log；启动全量后前端源码未再修改。本批所有session均已取得终态，diff check通过。SDK最近全量仍为G4.47历史1507，Go最近仅G4.59 build/vet，不能视为当前完整联合验收。
- 下一项有当前源码证据：普通SDK工具审批卡未用viewer门禁/同步锁，工作台resolve回调没有人工请求键且失败后不刷新；Runtime普通工具审批只有规则变更分支额外核对approver，公共事务/cache边界仍需复查。将与规则提案特殊权限和三执行模式恢复一起核定，不用业务审批的G4.54/55修复代替SDK审批审查。
- 未push/部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据，未安装OCI、修改系统策略或启动浏览器。Goal保持active，完整真实环境验收及最终独立全局review仍未通过。

### G4.59 运行控制目标、未知回执及事务授权 review（2026-09-09）

- 用户询问距离review完成多久；核对当前Goal和源码后没有给出未经核定的小时承诺。继续原运行控制范围，不新增产品方向。SDK源码、固定依赖和数据库schema未改，不将历史SDK1507项算作本轮重跑。
- **前端修复**：运行/步骤动作必须匹配当前快照的确切Run和当前Step归属；工作台同时检查项目当前写Run及viewer，目标不一致时刷新并拒绝，不再替换为另一个active_write_run_id。发送新Skill前释放失败Run复用同一控制入口。API验证enabled及目标类型/Run ID，步骤所属Run由工作台快照验证，保留原确认正文。
- 五类操作保留当前页面中的原请求键用于人工未知结果重试，成功或明确拒绝后释放；后续暂停/继续循环使用新键。按Run同步防止竞争提交，回执Run ID不一致时拒绝，不用旧操作快照覆盖新状态；所有提交结果都重新读权威projection。这个键是页面内状态，不宣称跨浏览器重载持久化。
- 运行控制表单按Run/Step/状态/动作集合及失败批次身份隔离；未知响应只允许重试原操作，竞争动作禁用。结束及部分继续的错误传回原确认窗口，不再被吞掉并当作成功关闭；提交期间关闭/返回/确认同步锁定。换任务或状态更新后丢弃旧窗口及迟到错误。
- **后端源码修复，Go行为未验**：公共控制在`beginExecutionControlCommand`事务内核对当前用户/成员/项目及scope，再读取幂等回执；Run/后台任务回执核对确切目标和项目。SDK原审批及工具身份门禁不变，保留原HTTP请求hash。`ContinueWithPartialResults`也走公共门禁，拒绝未经控制工具审批的Agent调用；未结束项检查增加repair_pending/waiting_approval/paused，与动作投影保持一致，并处理迭代错误。
- 新增Go测试源码：九类公开控制的fresh/cached降权、当前成员/用户/工作区/项目失效、跨域scope/目标回执、请求hash变化和未批准Agent拒绝；缓存测试明确使用合成回执，只隔离授权/绑定，不冒充完整转换成功。扩展原视频批次生命周期，插入第三个未结束项，检查部分继续拒绝且不改事件/运行，再移除夹具并沿原流程继续。均未执行，不绕过Application Control。
- 首次前端专项187项中177通过/10失败，`.tmp/goal-g459-controls-initial.json`/log；为原传输夹具省略目标字段、新用例误把普通Error当作可直显错误、同一个act中连续访问尚未渲染的菜单所致，修正夹具和交互顺序，不把它们列为另外十个产品缺陷。随后5文件187项通过，32.99秒，`.tmp/goal-g459-controls-after.json`/log。
- TypeScript退出0，`.tmp/goal-g459-ui-types-final.log`；隔离Vite退出0、2.25秒，只输出`.tmp/goal-g459-ui-dist`，保留515.76kB主chunk提示，未调阈值或改用户服务。最后Go全后端build和六包vet退出0，`.tmp/goal-g459-build-final.log`、`.tmp/goal-g459-vet-final.log`；这不是Go行为验收。前端最终全量54文件532项通过、0失败/待定，211.37秒，`.tmp/goal-g459-ui-all.json`/log；全量启动后前端源码未再修改。本批所有测试/构建session均已取得终态，diff check通过。
- 未push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据，未安装OCI、修改安全策略或启动浏览器。完整回归、真实环境及最终独立全局review仍未通过，Goal保持active；后续按同一交付清单核对后台控制和提案执行的重试恢复，不缩小原范围。

### G4.58 返修回执、交接集合及重载恢复 review（2026-09-09）

- 上一轮G4.57为progress，本轮继续原编辑/返修用户闭环，没有新增产品方向。SDK源码、依赖和数据库schema未改；不把历史1507项SDK测试算作本轮重跑或真实Go联合。
- **后端源码接通，Go行为待验**：运行快照增加可选`pending_script_edit`，从当前Run/Step、项目写任务、持久脚本/交接版本和Task绑定还原完整待刷新版本及scope集合。复用冻结能力版本的整步剧本合同，状态不一致时显示不可操作原因；能力版本不可用不再使整个作品快照无法读取。普通快照和事务内快照都接入，HTTP项目投影保留该字段，不依赖保存回执、本地缓存或新增数据库状态。SDK现有`get_run_snapshot`/`inspect_run`透传JSON，不需要另改模型循环。
- `CompleteScriptEdit`新命令额外检查该Run仍是项目当前写任务；合法已完成回执仍在状态门禁之前读取，保留G4.56恢复行为。待刷新查询同时绑定产物所属项目和版本所属产物，未放宽版本集合/CAS、当前权限或任务状态检查。
- 接受返修后的HTTP交接不再仅提交刚改的一集，而是使用接受回执中的完整`pending_refresh_version_ids`。校验本次保存版本必在集合中，拒绝空ID/重复/不完整多集集合；旧单集回执兼容，旧多集缺失集合时明确失败，不猜测。接受和HTTP交接仍是两阶段，但失败后可从当前持久快照恢复交接，无须重存正文或重复接受。
- **前端修复及夹具回归**：修正`RevisionAcceptResult`的字段名为实际后端`revision_request`/`version_result`，接受/拒绝/取消按原修改请求和版本保留人工重试请求键。回执核对项目、会话、请求ID、版本、状态和产物；旧projection不能回退已知较新返修状态，已终结返修不继续挂在预览中。提交时同步防重复，结果未知时锁住竞争操作，旧表单迟到错误不进入新版本表单，viewer不显示返修写操作。
- 用户重载页面后，在对话和进度视图均可看到“更新交接”，读取服务端完整版本集合并使用原完成编辑API；同一集合重试复用请求键，新的集合更换身份。核对当前项目、Run、Step及回执目标，不以重新保存正文代替恢复。增补三个交接相关事件触发投影刷新；只读、错Run、不可完成、重复版本均不提交。运行卡的只读写操作门禁同步补上。
- 首次专项77项中70通过/7失败，`.tmp/goal-g458-recovery-initial.json`/log。定位空态漏算已有Run、终态返修被整体过滤导致用户看不到结果两类显示问题；修正空态，保留有限的近期终态返修与展开历史，不用空聊天夹具掩盖入口缺失。扩大保存、返修和运行控制专项后125项通过，`.tmp/goal-g458-recovery-after.json`/log。
- 最终前端全量53文件505项通过、0失败/待定，261.47秒，`.tmp/goal-g458-ui-all.json`/log。包含完整App的多集重载恢复、已保存但HTTP交接失败、原请求人工重试、错误回执拒绝、版本集合变化，以及组件双击/迟到响应隔离。TypeScript退出0，`.tmp/goal-g458-ui-types-final.log`；隔离Vite退出0、1.95秒，仅输出`.tmp/goal-g458-ui-dist`，`.tmp/goal-g458-ui-build.log`。保留513.27kB主chunk提示，没有提高阈值；没有浏览器截图，CSS静态/构建结果不冒充视觉验收。
- Go新增完整版本集合的HTTP辅助函数用例，并扩展原完整剧本生命周期测试：普通/事务快照及JSON字段包含正确恢复集合，Task绑定不一致时禁用、非当前写Run不显示/不执行，完成回执进入running后不再含待完成入口。全后端build及六包vet退出0，`.tmp/goal-g458-build-final.log`、`.tmp/goal-g458-vet-final.log`；Go行为测试受Application Control限制未执行，不能将新增测试源码算作通过。
- 所有本批测试/构建session均已取得终态。未push、部署、操作测试机/8860/8880/用户作品或数据库/网关/凭据、安装OCI、修改安全策略或启动浏览器。旧版已经部分提交的业务记录未在本批迁移或修写。
- **下一步仍在完整范围内**：本轮只读复核发现工作台运行控制用`project.active_write_run_id`拼装暂停/恢复/取消请求，未先核对动作的确切Run目标；API已有可选请求键，但该入口未传入。接下来联合核对运行/步骤控制的目标、手动重试、当前授权与回执，不能用G4.57/58编辑链修复泛化成所有写命令已审完。外部Go/OCI/网关/浏览器验收仍为未通过，Goal保持active，不签署全局review完成。

### G4.57 返修接受事务与终态回执 review（2026-09-09）

- 用户询问距review结束多久；本轮核对active Goal及当前源码后继续修复，没有另给未经核定的小时数，也不把已知两个待收口环节说成全范围仅剩两项。
- **源码事务修复，行为待验**：提取现有普通版本、通用版本及影响决策的事务内实现，公开方法仍自行管理事务。`ApplyArtifactVersion`现在将保存与下游计划一起提交；`AcceptRevision`复用同一事务，再写接受状态、审计事件和原命令回执。任一步失败均由外层回滚，保留业务审批、下游重生成与通用产物直接确认的原规则，没有新建接受中状态、迁移或执行真实下游任务。
- **源码授权与重试修复，行为待验**：接受、拒绝、取消先在事务内验证当前项目/工作区/成员及写权限，检查命令范围后读缓存。接受回执核对修改请求ID、项目、会话、期望版本、终态和原接受事件绑定的确切产物/base/结果版本；当前产物后来有新版本时仍允许读原接受回执。新接受同时检查产物属于该项目、base属于该产物；拒绝/取消使用原命令幂等记录并绑定终态，竞争失败不生成新版本。内部无请求键的调用不再凭空派生`:artifact`键。
- 新增Go回归源码覆盖普通/工作流接受状态、接受事件、回执提交故障注入及同键重试；工作流检查原下游计划仍生成。另含三类终态命令fresh/cached的六类当前授权检查、错误范围/版本/请求回执拒绝、接受与拒绝/取消/另一接受竞争、后续版本不覆盖原回执、错绑回执和跨项目/base拒绝，以及公开保存下游计划失败时回滚。故障测试核对实际注入错误，不以任意失败替代目标故障。未执行这些Go测试；未使用或绕过被Application Control拒绝的测试程序。
- 当前后端全量build退出0，`.tmp/goal-g457-build-verified.log`；六包vet最终结果见`.tmp/goal-g457-vet-final.log`。前端、SDK及依赖本批未改，未重跑；G4.56前端480项与G4.47 SDK1507项仅为各自历史证据，不算本批Go联合回归。
- **未收口部分明确保留**：`completeAcceptedRevisionHandoff`仍只发送本次结果版本，未消费完整`pending_refresh_version_ids`；返修界面跨人工重试的请求键和重载后的已保存/待刷新权威入口仍待核对接通。HTTP交接仍在接受事务之外，不能将Runtime原接受回执可重读说成整条用户任务可恢复。旧版已部分提交的记录没有在本批迁移或修改。
- 本批只修改本地源码和测试/进度文件，不push、不部署、不操作测试机/8860/8880/用户作品/数据库/网关/凭据，不安装OCI、修改系统策略或启动浏览器。Goal保持active；实现、行为验证及最终全局review分别记账，不签署完成。

### G4.56 产物保存、交接完成及展示版本绑定 review（2026-09-09）

- 上一轮G4.55为progress，本轮继续原用户编辑任务，不新增产品方向、不缩小原验收范围。SDK源码及固定依赖未变，没有把历史SDK1507项称为本批重跑或真实Go联合。
- **保存入口源码补接，Go行为待验**：HTTP保存原来统一调用`ApplyArtifactVersion`，后者直接进入需要业务审批模板的低层保存，普通generic_document/generic_table会走错路径。现按已有agent_shell通用产物合同接`CreateConfirmedGenericArtifactVersion`，显式用户保存直接创建确认版本，不伪造Business Run/审批，也不为每个Skill增加执行分支。
- **版本和完成命令源码修复，Go行为待验**：普通及通用版本保存、`CompleteScriptEdit`在事务内复用现有当前项目/成员校验，检查范围后才写入或读缓存。版本回执绑定产物、持久结果版本及确切base版本；完成编辑回执绑定Run、原事件游标和每个刷新Task对应的脚本版本。状态检查移动到合法缓存读取之后，因此已从waiting_approval进入running也能重试，不重新排队。原HTTP请求哈希和缓存格式保持，新增`pending_refresh_version_ids`为可选回执字段，无数据库迁移。
- **完整待刷新集合**：保存事务返回当时所有待刷新交接的确切脚本版本ID，与scopes一起由同一查询取得；前端使用该集合，不仅发送刚编辑的一集。旧单集回执仍兼容；旧多集回执若没有完整版本集合则明确拒绝猜测。后续变化仍由Runtime集合/CAS校验拒绝，未放宽并发保护。
- 新增24组普通/通用、fresh/cached、六类撤权或隔离拒绝的Go测试源码，检查无额外版本、审批、事件、回执或项目版本变化；通用文档/表格各补公开保存路径、同键重试、跨产物/基线版本/范围拒绝。扩展原完整剧本流程用例，检查版本集合、fresh/cached撤权拒绝、进入running后的原完成回执及错误版本集合拒绝。均未运行Go测试，只经build/vet，不计为行为通过。
- **前端保存恢复，已专项回归**：保存时冻结产物、base版本、正文和请求键，提交入口用同步锁防重复。保存结果未知时重试同一命令；已经得到版本回执后仅重试交接完成，得到完成回执后不重做该阶段。提交或结果待确认期间锁定正文/标题/格式操作，保留正文和错误；用户显式确认后可放弃本地待确认状态并刷新，但不声称回滚服务端版本。普通和视频编辑器复用同一状态，viewer不再得到直接编辑入口，格式/状态工具栏允许换行；未启动浏览器，CSS检查不是截图验收。
- 编辑期间远端新版本不能清空本地草稿或偷偷换CAS基线。已收到的保存版本用于当前显示，不让尚未刷新的旧projection将其回退；历史版本及当前正文通过子组件回报给Composer，未保存草稿/修改提案不冒充已保存版本引用。保存回执的产物/版本及交接回执的Run均核对，错误回执不触发后续命令。这些版本绑定在最后一轮全量中继续核验。
- 初始工作台48项中42通过、6失败，`goal-g456-save-before.json`/log；首修与文本/视频编辑器专项60通过，`goal-g456-save-after.json`/log。扩大格式、审批、传输、远端更新和刷新确认场景后125通过，`goal-g456-save-expanded.json`/log；随后补完整待刷新集合，以及已有历史下载用例中的对话引用检查。
- 首轮前端全量480通过、0失败/待定，`goal-g456-ui-all.json`/log，但执行过程中继续修改了展示版本接线，不当作最终同版证据。该进程长时间未结束时通过只读标识核对本轮Vitest及日志；准备停止旧轮前，进程已自然结束、原session取得exit 0，停止前置检查因此拒绝执行，实际没有停止任何进程。未以观察超时判定退出，也未操作另一未识别的Node进程。最后带verbose/json双报告的同版全量50文件480项通过、0失败/待定，386.65秒，`goal-g456-ui-all-final.json`/log；完整App历史版本引用与未保存草稿不绑定持久版本的断言亦通过。
- 当前源码TypeScript已退出0，`goal-g456-ui-types-verified.log`；隔离Vite退出0、4.50秒，仅输出`.tmp/goal-g456-ui-dist`，`goal-g456-ui-build.log`。保留507.03kB主chunk提示。全后端build及六包vet退出0，`goal-g456-build-final.log`、`goal-g456-vet-final.log`；后者晚于最后Go用例修改。未运行受限Go程序、真实OCI或网关，没有改策略、用户数据或运行环境。
- **本批不冒充完整编辑/返修验收**：保存重试状态当前在组件内；页面重载后，已保存但尚未提交交接刷新的状态还需要可恢复的权威入口。`AcceptRevision`仍先调用版本保存，再用另一事务更新revision状态，必须继续核定与拒绝/取消竞争、部分提交及接受重试的一致性。它们是原用户任务剩余问题，不归咎于外部环境，不列为已解决。
- 所有本批测试/构建命令均已取得终态，限定diff检查通过。未push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI、修改系统策略或启动浏览器。Goal保持active；本轮为progress，不签署全局review通过。

### G4.55 续写交付、审批事务及质量审核编辑 review（2026-09-09）

- G4.54后已开始本批下载和后端修复；中间用户询问时间的轮次只核对状态，不计作开发进展。本轮继续原交付目标，没有将测试数量当作完成比例，也没有新增产品方向。
- **下载源码补接，前端已回归、Go行为待验**：`ArtifactWorkspace`对已保存产物提供下载入口，不再仅限generic_document/generic_table。后端从不可变版本的有效JSON生成交付清单：所有类型保留完整JSON，明确正文字段提供TXT/MD/DOCX，完整矩形表格提供CSV；支持script_text，因此续写和新插件结果无需再加Capability ID分支。多个相异正文不猜测选择，仅交付JSON；保留原数值字面量、CSV公式防护、文件名和XML检查。未保存草稿/修改提案仍不能冒充持久版本下载，不把JSON导出等同二进制包交付或主机目录写入。
- 三个完整App夹具分别覆盖续写、动态脚本、动态结构化产物：无script_candidates也有入口，当前版本和历史版本各自绑定正确ID/文件名，实际下载走认证API及Blob，不采用返回的非受信URL。初始35项中32通过、3失败，`goal-g455-delivery-before.json`/log；补接后与下载/API专项共49通过，`goal-g455-delivery-after.json`/log。后端增加十类动态输出合同测试，并扩展原续写三阶段流程测试，检查完成时下载以及修订后原版/新版正文互不串用；这些Go用例均未执行。
- **审批和返修源码修复，Go行为待验**：`ResolveApproval`、`RequestApprovalRegeneration`、`RequestApprovalRevision`在事务内写入或返回缓存之前，复用当前成员授权，核对用户/工作区/项目存续、角色及命令范围。缓存绑定确切审批来源：普通确认使用原回执事件游标定位已解决审批，重生成和SDK返修使用现有审计事件关联计划/返修ID。保留HTTP原方法/路径/正文请求哈希，不新建缓存格式或迁移，也不把Runtime审计actor误认为用户作者。返修取目标同时限定项目、Run、Step。
- SDK返修要求原审批Options允许request_ai_revision，质量审核和最终选择仍使用专用处理接口。原合法返修回执先于pending检查读取，审批随后过期时同键仍能取回已创建请求，不重新写消息/请求。新增36组fresh/cached撤权场景、同Run跨审批回执拒绝及scope检查、动作限制/过期后原回执和三维目标归属测试源码；仅静态检查，不称通过。
- **质量审核编辑归属，已复现并完整App回归**：原手动修改仅按script_unit和集数找产物，可能打开另一Run的旧稿。现限定审批所属Run，并在编辑时重新读取后核对审核ID、项目、Run、版本和输入hash；没有匹配目标时不回退旧Run。初始42项中35通过、7失败，`goal-g455-quality-before.json`/log；修复后App/审批组件/API专项80通过，`goal-g455-quality-after.json`/log。该80项没有包含下载组件文件，完整下载组件由下方全量覆盖，不合并夸大专项范围。
- 最终前端全量467项通过、0失败/待定，`goal-g455-ui-all.json`/log；TypeScript退出0，`goal-g455-ui-types.log`；Vite退出0、1.56秒，`goal-g455-ui-build.log`，仅输出`.tmp/goal-g455-ui-dist`。保留504.43kB主chunk提示，没有提高阈值隐藏。全后端build退出0，`goal-g455-build-verified.log`；新增Go用例后六包vet退出0，`goal-g455-vet.log`。先前build调用的退出结果在上下文恢复时不可见，空旧日志不算成功；只读确认无go进程后才以新命令取得终态，未重复执行Go测试。
- 本批全部测试/构建命令均已取得终态。SDK源码未改未重跑，历史1507项不当作本批联合结果。未push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI、修改策略或启动浏览器。
- **下一步按同一用户任务继续**：只读检查发现`ArtifactWorkspace.save`的保存版本和handoff完成是两个请求；API仅在单次调用内部重试时保留请求键，用户再次点击会换键。普通产物版本Runtime目前直接解码缓存，需核定未知提交结果、当前授权、确切版本和handoff失败后的恢复；返修接受/拒绝链同样待审，不将本批审批修复泛化成所有写入都已审完。Goal保持active，本轮为progress，完整review和验收仍未通过。

### G4.54 共享审批卡及确认后运行恢复 review（2026-09-09）

- 上一轮G4.53为progress。本轮继续原四流程的用户任务检查，核对原计划完成态和W0/W5；没有把静态合同通过当成用户操作通过，没有新增产品方向。
- **审批内容与草稿绑定，已修复并组件回归**：审批卡按请求/运行/版本/状态/对象版本/hash/动作集/受信视图重新建立内部状态。候选、选择、修改草稿和错误不会带到新审批；旧读取及旧提交错误不能污染新卡。候选读取核对确切版本ID和版本号，质量审核核对审核ID/版本/输入hash/所属Run；错误及格式不完整有明确重试入口，不再永远显示加载中。
- **通用单选与操作门禁，已修复并组件回归**：`single_option`不再强制五个候选，允许符合通用候选显示结构的动态Skill使用其他数量；唯一编号、可读标题及文本字段仍检查。续写必须五方向的规则保留在G4.53合同和Runtime产物校验中，没有放宽其业务要求。提交期间锁定候选/配置，函数入口防重复；不提供未在审批动作集中声明的提交按钮。非pending记录及viewer只读，竞争返修到达后阻止已打开编辑表单提交。原多选改编、扩写策略、逐集/连续确认、质量审核风险/返修分支有兼容测试。
- **确认后恢复归属及重试，已修复并完整App夹具验证**：使用审批自带run_id，不回退猜测项目当前运行；读取结果和resume动作目标必须属于同一Run。成功或失败后刷新权威projection，同一确认/返修/质量处理/最终选择/连续模式/恢复阶段复用请求键，审批版本或命令改变才换键，展示标题变化不会换键。API可接收显式请求键，11类命令传输测试验证重试的键和正文稳定。这不证明Go幂等与当前授权已经完整通过。
- 初始审批组件16项中2通过/14失败，`goal-g454-approval-before.json`/log。失败包括版本残留、候选格式/归属未验证、固定五项、提交未锁定及非pending/竞争返修门禁；其中读取错误的精确文字断言使用普通Error，被既有errorText规范化，后改为实际ApiError夹具，不计为另一产品缺陷。初修后16项通过，`goal-g454-approval-after.json`。完整App确认链先30项中26通过/4失败，`goal-g454-approval-chain-before.json`/log，分别复现错误Run、缺少Run时猜测、错误快照和未知请求重试问题。
- 修复后专项130项通过，扩大其他审批分支及传输后152项通过，`goal-g454-ui-focused.json`和`goal-g454-ui-expanded.json`。再补动作目标和viewer整体App检查、请求键展示/版本变化断言后，最终前端全量457项通过、0失败/待定，`goal-g454-ui-all.json`/log；TypeScript退出0，`goal-g454-ui-types-final.log`，Vite退出0、1.60秒，`goal-g454-ui-build.log`。产物只在`.tmp/goal-g454-ui-dist`；构建有504.17kB主chunk提示，未通过提高阈值或无关重构掩盖。所有本批进程已取得终态，限定diff检查通过。
- **下一步明确的交付缺口**：`ArtifactWorkspace`只给generic_document/generic_table显示下载，`artifact_delivery.go`也只接收这两类；续写产物因此没有版本下载入口。现有候选稿只在`executeScriptsAggregationTx`创建，续写三步不经过该聚合，不能用顶部候选稿导出当作续写已有交付。需补与通用产物/受信展示一致的文件交付，而不是为每个新Skill再写Capability分支。
- **后端审批继续审查项**：只读检查`ResolveApproval`、`RequestApprovalRegeneration`和实际HTTP分流到`RequestApprovalRevision`的路径；下一步核定事务内当前成员授权、作用域/缓存回执绑定及动作白名单。当前`RequestApprovalRevision`没有显式检查原审批Options是否允许request_ai_revision；不能用前端按钮隐藏代替后端限制。质量审核手动编辑仍需核对目标Run筛选。上述本批未改、未运行Go，不列为已修复。
- 本批未改Go/SDK产品源码，未重跑其build/vet/全量，不把历史SDK1507或本次前端夹具称为同场E2E。未push/部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI、修改策略或启动浏览器。Goal保持active，本轮为progress，续写交付及后端确认边界继续推进，最终review仍未通过。

### G4.53 原四流程合同验收覆盖修复（2026-09-09）

- 用户再次询问距离review完成多久。核对当前清单后，仍不能给出可信的小时倒计时；代码审查/修复尚未收尾，Go执行策略、OCI和网关/用户页面真实验收与代码缺口分别记录。没有将已撤回的历史估时再次作为交期，也没有扩展产品方向。
- 新增`acceptance/validate-capability-contracts.test.mjs`，仅复制受信校验器及Capability/Schema/提示词资源到仓库`.tmp`隔离夹具。先验证临时根目录归属、拒绝输入链接，再修改副本；不操作用户数据或服务。第一次Node测试因子进程`spawn EPERM`未进入用例，不算产品反例；经工具权限复核后32项中3通过/29失败，旧门禁即使删除续写manifest、选项审批、提示词或改错结果适配器也报告通过。证据`goal-g453-contract-before.log`与`goal-g453-contract-before-reviewed.log`分别保留。
- **门禁源码已修复**：固定清单纳入第四个manifest及三步顺序，校验文件名和Capability ID一致，支持已有Runtime/UI的`select_single_option`。续写必须经过原稿、五方向选择、正文三阶段；输入绑定确认版本，选择审批和新版本失效规则保留，正文包含决策快照，最终产物经审批确认才完成。检查配置1000至50000整数范围、五选项Schema、选择ID及输出适配器目标，不把这些业务不变量称为完整JSON Schema引擎，也不在Runtime增加Capability ID分支。
- 修复后实际校验输出`Manifests: 4; Steps: 37; Response adapters: 21`，`goal-g453-contracts.log`；隔离32项全部通过、0跳过，41.564秒，`goal-g453-contract-after-reviewed.log`。发布脚本`capability_contracts`动作接入这组自测，矩阵明确仅静态合同证据。独立PowerShell报告/预检查夹具51项通过，`goal-g453-release-evidence.json`/log；日志中的`go_test failed`为显式抛错的夹具，不是执行Go。
- 原Stage8计划校验仍为99/99、5套件/3环境，`goal-g453-stage8-plan.log`，只证明旧计划分配完整。本轮只读检查了实际`script_continuation_execution_test.go`：已有原稿审批、五方向单选、decision快照、正文审批完成及完工后修订测试；另有长度下限测试。它们不是未开发，但当前受限未运行，也不是SDK/用户页面同场证据。前端已有长度配置及视图分配测试，尚需核对单选操作到结果交付的联合覆盖。
- 本批未改Go、SDK或前端产品源码，未重跑其全量；最近前端413及SDK1507仍标原批次。所有本批命令终态已取得，限定diff检查通过。未运行完整发布门禁或回滚演练，未push/部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI或修改策略。Goal保持active，本轮为progress，最终review仍未通过。

### G4.52 冻结Schema、启动事务及确认重试收口（2026-09-09）

- 上轮G4.51为progress。本轮核对当前源码后继续同一原业务确认链，没有新产品方向；不把原审计中的历史“未实现”直接当作当前状态，也不以新的测试数给完成比例。
- **Schema与提案绑定，源码已修复、Go行为待验**：配置GET携带`proposed_action_id`，Runtime核对作品/Capability/版本/提案完全一致，在只读事务中检查项目及当前成员可读权限，然后通过原invocation解析冻结包并投影Schema；不再按同名同公开版本的活动目录选包。普通无提案的目录读取保留原语义。扩展同版本不同Schema用例、错误项目/能力/版本/提案拒绝、viewer可读但撤权拒绝，以及实际HTTP路由接线测试源码，均未运行Go测试。
- **前端Schema读取门禁，已组件回归**：未读到绑定版本、读取失败、版本/Capability不匹配或状态不可用时，保存/启动按钮禁用，函数入口也拒绝；错误可重试，草稿保留。配置默认项来自冻结包而非当前菜单版本，用户后续显式选择保留。未知视图仍只读，没有把manifest视图名变成任意代码执行入口。
- **三个启动事务与回执归属，源码已修复、Go行为待验**：传统Run、managed workflow及background Task在beginIdempotency前读取原提案、复查项目/会话/Capability、原身份与当前editor权限及Skill可用性。缓存结果必须对应该提案实际consumed_run_id或consumed_task_id和确切项目/会话，不能把另一启动的成功JSON当作当前回执；沿用原确认hash、CAS及Run/Task机制。
- 新增`proposed_action_start_test.go`包含三模式×首次/缓存×四类撤权共24个权限场景、六类消费目标/项目/会话回执场景及viewer/撤权读取场景。夹具静态复核纠正了workspace Skill安装需admin角色及managed来源类型；这两项是测试夹具修正，不是修改生产权限或业务合同。全部Go行为用例未执行，最终全后端build与六包vet退出0，`goal-g452-build.log`、`goal-g452-vet-final.log`。
- **旧投影及未知启动重试，已完整App组件回归**：收到直接配置回执时使此前开始的projection读取失效，再刷新当前快照；消费回执前也使旧刷新失效。同一规范化启动命令及旧消息响应后的bind/configure阶段各保留请求标识，重试复用，提案确认快照变化才换键。旧兼容消息绑定失败后两次提交仍用原消息/绑定键，页面只有一个用户消息；这些是受控API夹具，不是真实Go/浏览器E2E，旧视频自动配置目前为代码接线和API键契约证据。
- 初始UI67项中62通过/5失败，`goal-g452-ui-before.json`/log：四项复现提案绑定/投影/启动键问题，一项旧停止测试未等异步提交回调就调用reject。修正后专项99通过。加Schema加载门禁后13项旧测试因在读取完成前点击而失败，保留`goal-g452-ui-definition-gate.json`；改为等待按钮可用，同时新增显式“加载/失败期间不能提交”和重试保持草稿的反例。之后108通过，补冻结默认项和完整旧消息绑定重试后最终专项110通过，`goal-g452-ui-final-focused.json`/log。
- 最终前端全量413通过、0失败/待定，`.tmp/goal-g452-ui-all.json`/log；TypeScript退出0、Vite隔离构建退出0，`goal-g452-ui-types.log`、`goal-g452-ui-build.log`，产物仅`.tmp/goal-g452-ui-dist`。SDK源码未改，未重跑1507项，不拼成同版Go/SDK真实联合。所有本批相关进程已取得终态，限定diff检查通过。
- **原四流程门禁仍有覆盖遗漏**：只读检查并执行两个不启动进程/网络的Node校验器。Capability合同校验退出0，但实际输出`Manifests: 3; Steps: 34; Response adapters: 21`，固定数组漏`script-continuation.json`；Stage8计划校验退出0、99/99分配，只证明旧计划/fixture文件完整，不能证明当前原四流程运行。证据`goal-g452-capability-contracts.log`、`goal-g452-stage8-plan.log`。下一步补原范围校验与场景覆盖，不能凭这两项绿色结果宣布原四流程通过。
- 继续原W0至W9、SDK/GAP和用户任务审查；真实Go/OCI/网关、用户浏览器与完整资源/配置/部署回滚仍未验收。未push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI或修改系统策略。Goal保持active，不签署最终review。

### G4.51 原业务配置确认与提案事务 review（2026-09-09）

- 上轮G4.50是progress；本轮继续原范围，没有增加产品方向。用户询问review还需多久：未给未经核定的小时数，明确代码缺口与外部验收不同，不能说只剩最终签字。四条原流程核定为`novel_to_script`、`non_novel_to_script`、`video_reference_creation`、`script_continuation`；旧four-mode烟测实际只覆盖前三条加通用文档，不是这四条同版验收，且默认访问用户环境，本轮没有执行。
- **配置卡旧值与迟到保存，已复现并组件回归**：`RunConfigurationCard`按作品、提案ID、版本、状态、动作及快照绑定内部表单生命周期。相同版本刷新保留未保存草稿；新版本使用服务端配置；旧表单迟到响应不再调用父更新。父直接回执只推进仍pending且版本更低的同一动作。不能据此宣称所有projection与本地更新竞争均已解决，剩余窗口见下。
- **重试身份和失效入口，已复现并组件回归**：保存配置按规范化命令复用请求标识，手动相同重试沿用后端幂等回执，修改内容才换键；API显式传递该键。请求期间锁定数值、材料、单选、Schema/JSON输入。superseded/expired/cancelled不再展示可编辑配置或启动按钮，保存函数也检查pending及动作类型。没有自动启动、重发模型或改SDK循环。
- **提案事件缺失，已补接线并组件回归**：监听`proposed_action.created/configured/input_bound/consumed`刷新权威projection，不依赖另一个Run事件。测试使用受控EventSource及既有120ms防抖，不是浏览器/真实Go SSE验收。
- **冻结Skill选择及事务授权，源码已修复、行为待验**：`ConfigureProposedAction`原先最后才设置action ID，之前两次冻结版本解析均收到空ID而退回当前目录。现在查询前即绑定原ID。配置和输入绑定在同一事务内检查项目存续、当前工作区成员/用户/角色以及原身份editor权限，缓存回读前同样检查；范围不一致或幂等回执属于另一提案均拒绝。无显式身份的受信内部调用约定保留，不扩展执行权限。
- 新增`proposed_action_mutation_test.go`：两种操作各六类撤权/删除/跨工作区场景，覆盖新写/缓存回读、同键跨提案、错误scope和拒绝后无变更；另有同公开版本的高优先级Skill改变Schema、冻结提案仍使用原Schema的用例。全部Go行为测试**未执行**。只读复核修正了输入夹具缺失的user_request_message_id及原turn请求一致性；最终全后端build退出0（`goal-g451-build.log`）、六包vet退出0（`goal-g451-vet-final.log`），不是Go测试通过。
- 第一轮前端64项中52通过/12失败，`.tmp/goal-g451-ui-before.json`/log；首次修复60通过/4失败，其中一项标签应匹配含“分钟”的时长标签，三项遗漏推进既有事件防抖时间。修正这些测试写法后含API契约的专项96通过，`goal-g451-ui-focused.json`/log。前两份失败记录保留，不把夹具写法错误算成额外产品缺陷。
- 最终前端全量399通过、0失败/待定，`.tmp/goal-g451-ui-all.json`/log；TypeScript退出0、隔离Vite构建退出0，`goal-g451-ui-types.log`、`goal-g451-ui-build.log`，产物仅在`.tmp/goal-g451-ui-dist`。SDK本批未改未重跑。所有本批测试/构建进程已取得终态，没有新浏览器或真实服务证据。
- **下一步仍在同一确认链，不是全局review通过**：配置Schema GET目前仅按作品/Capability公开版本读取，未绑定确切提案的执行快照，同版本覆盖时仍可能显示另一份Schema；StartRun的传统/managed分支及StartAgentTask在幂等回读前缺少同事务当前权限复查，需核对回执与原提案归属；前端启动手动重试及旧`applyExchange`自动绑定/配置仍生成新键。`load`开始后、直接配置回执先到达的投影竞争也需补完整App测试，当前父回执版本判断不覆盖该方向。
- 保留原回滚保障；未push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI或修改系统策略。外部阻塞未变化，不重复尝试受限Go测试。Goal保持active，本轮为progress，不签署最终review。

### G4.50 带业务数据的回滚校验及独立文件恢复（2026-09-09）

- 上一轮修改发布脚本并完成51项报告夹具，分类为progress。本轮继续G4.49的明确缺口，没有新增产品方向；只读核对固定基线schema24、实际Runtime迁移回执、文件存储引用、产物/消息读取接口及当前脚本。没有执行Go二进制或用户服务。
- 新增`agent_platform_rollback_data.py`，只允许CLI操作`.tmp/agent-platform-rollback-drill/<run>`内的临时数据库，拒绝越界/reparse point、已有业务数据、已有清单及非schema24基线。夹具包含两个工作区/作品、两个会话、四条独立消息（中文与重复正文）、两个产物/四个历史版本、运行/输入/配置快照、资产/原始文件、事件及幂等回执。文本和二进制原始字节另存独立备份；不读写用户数据。
- 备份选择从`migration_history`读取唯一的completed回执，校验from/to/schema和migration_id，要求备份引用确切匹配对应迁移目录与数据库文件名。缺失、多条、未完成、版本错误、路径不匹配均拒绝，不按文件时间选择。对备份执行SQLite完整性/外键检查，与迁移前全部表、schema、类型和重复行的逻辑摘要比较；VACUUM导致页布局和物理hash变化不被误判为业务丢失。
- 迁移后核对所有夹具业务字段及行数，允许新schema新增列，但不允许旧内容变化、历史版本丢失或复制出额外消息。恢复使用SQLite backup API及独立文件备份写入全新`restored-data`，拒绝已有目录，不依赖迁移后原文件树；最终再核对完整逻辑摘要、原始字节及备份/清单未变化。夹具实际删除临时原文件后仍恢复成功，损坏独立备份则在创建恢复目录前拒绝。
- 生产演练脚本已接线：旧二进制建库/停止→写夹具→旧版API读验/停止→当前二进制迁移/停止→核对原业务数据及确切备份→旧版拒绝高版本→新目录恢复→旧版API再次读验/停止→完整快照/文件核对→清理后签署。本轮**未运行该真实链路**。前后各12项只读API检查覆盖作品、消息、产物版本和原文件下载，禁用代理/重定向、限制大小并拒绝8860/8880；当前HTTP测试是随机loopback假服务，不是Go API验收。
- v2报告scope更新为`baseline_business_database_assets_and_read_api`，逐个业务阶段保存状态，异常阶段failed；`business_data_validation`只有实际链路完成才passed，`rollback_ready`仍false，不代表所有用户数据、完整资源/配置和部署版本可回滚。发布门禁已纳入新Python回滚辅助自测，旧schema-only文案更新；此前G4.49描述保留其当时语义。
- 首轮标准库unittest25项通过、53.925秒，`.tmp/goal-g450-rollback-data-first.log`；增加真实CLI参数链和外键损坏拒绝后27项通过、59.793秒，`goal-g450-rollback-data-final.log`；增加迁移重复消息计数后最终28项通过、64.139秒，`goal-g450-rollback-data-verified.log`。初始DDL提取自固定基线的真实SQL常量，VACUUM/SQLite backup均实际执行，但迁移回执是测试构造，**没有执行Runtime迁移器**。
- 最终PowerShell报告/语法/缺工具链副本预检查51项通过，`.tmp/goal-g450-release-evidence-final.json`/log，经工具权限复核仅允许原子报告替换夹具，没有修改系统策略。Go/SDK/前端产品源码本批未改，未重跑其全量，不把历史1507/386计为本轮结果。相关进程均已取得终态，限定diff检查通过。
- 没有push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI或更改ACL/安全策略；原回滚备份未触碰。代码侧回滚校验缺口已补到演练入口，但真实二进制、完整资源/配置恢复、原业务四流程同版E2E与最终review仍未通过；Goal保持active。

### G4.49 发布与回滚证据 review（2026-09-09）

- 用户再次询问距离review完成多久。本轮核对当前Goal和交付清单，不继续引用已撤回的20至35小时，也不另给未经工作量核定的交期；代码审查剩余事项与真实环境阻塞分开。
- **发布报告假通过，已修复并夹具验证**：旧脚本允许矩阵删掉/降级必需检查，且仅凭external_gates配置中的passed即可写ready。现在拒绝缺项、重复、非布尔required、降级及未知本地动作，并核验固定SDK版本；保留原11项检查，加入报告自测。外部声明保存为declared_status但状态始终unverified，本地脚本不签署release_ready或最终review通过。
- **历史成功和源码身份失真，已修复并夹具验证**：v2报告在预检查前写running，逐项保存pending/running/终态，预检查失败写failed；同时保留独立run_id目录，不覆盖旧SDK兼容历史证据。记录HEAD、dirty及运行前后源码内容SHA-256，包含未提交源码、测试、schemas、design prompts、Skill/Capability资源，排除运行数据/凭据/依赖/生成证据；输入矩阵单独核对哈希。源码/矩阵变化或未完成检查均拒绝签署本地成功。内容指纹不是不可变源码快照，也不证明外部依赖或部署同版。
- **验收覆盖用户构建，源码已修复**：前端typecheck及Vite构建输出至本次临时目录；审批烟测要求本次构建已通过，只使用指定产物，补Windows空格路径参数引用及确认preview退出。未实际运行浏览器或完整发布门禁，不称视觉验收通过。
- **回滚报告提前成功/环境泄漏，源码已修复，预检查失败有夹具证据**：开始即写当前报告，捕获预检查失败；GOTMPDIR/GOCACHE与已有配置一起恢复，演练子进程明确禁用原生工作区。清理逐个尝试，任何失败不妨碍其他进程和环境恢复；必须确认退出、完成所需清理后才写本项passed。报告JSON路径限定在仓库.tmp/docs/evidence并拒绝reparse point，递归清理前验证目标。真实Go进程终止/迁移/恢复仍未执行，仅语法及缺工具链预检查路径通过。
- **业务回滚仍未补齐，不冒充现有保障**：原脚本只创建空库、验证schema和备份复制hash，仍按时间找备份。v2明确`validation_scope=schema_and_backup_copy_only`、`business_data_validation=not_run`、`rollback_ready=false`。待补带项目/对话/产物等业务数据的恢复比对、精确migration_history引用和完整资源/配置版本恢复；没有执行或修改原备份、用户数据库及服务。发布手册同步schema63及真实验收边界，矩阵预发布项保留原流程、三模式、Skill作者闭环、用户UI和回滚范围。
- 首轮报告夹具47项中46通过，暴露PowerShell把File.Replace的null备份路径转换为空字符串；改用NullString后第二轮仍46通过/1失败，确认为默认沙箱拒绝原子替换。没有降级非原子覆盖或绕过系统策略；核验自建临时目录并经工具权限复核后47项通过，新增两个无工具链脚本副本预检查用例后49项通过，纳入必需自测门禁后最终51项通过、0失败，`.tmp/goal-g449-release-evidence-final.json`/log。前两次失败证据`goal-g449-release-evidence-tests`和`goal-g449-release-evidence-verified`保留，权限复核与入口扩展分别为`goal-g449-release-evidence-reviewed`和`goal-g449-release-evidence-entrypoints`。日志中的go_test失败是显式抛错的scriptblock夹具，不是执行Go测试。
- 源码指纹函数在实际仓库只读调用成功；最终51项覆盖缺项/降级、假外部通过、逐项状态、失败继续诊断、报告写入失败、源码/矩阵变化、原子替换、路径越界、reparse模拟、临时Git源码新增修改删除、两个实际脚本的缺工具链预检查失败及环境保留。没有执行生产门禁动作、Go二进制、浏览器或远端服务，不能作为完整回滚/发布验收。
- 本批只改验收脚本、矩阵及文档，没有重跑SDK/前端/Go全量，不重复计入历史1507/386结果。相关命令均取得终态，限定diff检查通过。没有push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据、安装OCI或修改系统策略；未触碰既有`start-local-demo.ps1`改动。Goal保持active，带业务数据的回滚、原范围同版联合、真实环境门禁及最终review仍未通过。

### G4.48 提交身份、未确认状态与接受事务 review（2026-09-09）

- 上一轮取得完整测试终态并修改断言/记录，分类为progress。本轮重新读取原完成态11条、W0-W9、SDK-01至14、UX-01至10及GAP记录；没有把既有实现清单反推为原需求。生产源码扫描未发现内置Capability ID分支或直接`.responses.create/compact`、`.chat.completions.create`调用，不能据此单独证明全部用户任务完成。
- **消息错误合并，已复现并组件回归**：原`mergeSnapshotMessages`和`mergeMessagesWithAgentTurns`用正文、时间窗口和asset ID匹配，可能吞掉独立的相同请求，还忽略快照/选区及排队修改。现在普通消息只按确切message ID合并；待提交消息通过用户、会话、请求键的版本化SHA-256标识关联到权威AgentTurn。接受、幂等读取和列表均派生同一公开`submission_id`，原Idempotency-Key仍不序列化；无schema迁移，也不把关联标识当作权限。已提交turn只有在对应持久消息到达后才撤下本地行。
- 旧服务或缺少WebCrypto的客户端不靠文字猜测确认，保留未确认行，等待直接POST回执或可验证的关联。正常安全上下文使用现有WebCrypto/哈希函数，没有新增依赖；真实WebCrypto组件用例覆盖HTTP回执丢失后从projection找回同一请求及随后提交，不重发模型请求。
- **HTTP中断误报SDK已取消，已复现并组件回归**：发送等待按钮改为“停止等待”，中断/网络5xx等未知结果保留`unconfirmed`，不显示“本轮已停止”。确切服务端turn仍用已有取消接口和终态。明确4xx拒绝与未知结果分开；未知结果只刷新权威projection，不自动重发。正文和材料沿用G4.47的同键恢复行为。
- **同键重试的旧传输覆盖新传输，已修复并组件回归**：乐观消息身份与单次HTTP传输身份分开。旧失败/中断不得覆盖新请求状态、缓存或选区；同一幂等提交的成功回执可以结算该提交。权威projection确认后移除对应本地缓存，不能在后续刷新复活旧消息。
- **接受事务缺少当前权限/回执归属校验，源码已修复、行为待验**：`AcceptAgentTurn`在新写入或读取幂等回执之前，复用`validateExecutionOwnerTx`核对当前成员、用户/工作区存续及编辑权限。幂等回读查询绑定workspace/project/conversation/user，不再把另一作者的同键请求当作本人的成功提交。没有改变历史幂等键或绕过原请求哈希。新增四类降权/撤权对fresh/cached路径的拒绝、状态不变和跨作者同键/独立新键用例源码，**未执行**。
- 前端七项新反例先在旧代码失败：42项中35通过、7失败，`.tmp/goal-g448-ui-before.json`/log。初步专项62通过，`.tmp/goal-g448-ui-after.json`；扩大专项首次62通过、2失败，原因是新的未确认草稿按设计跨挂载保留，而测试共用了同一项目ID。改用独立夹具项目后64项通过，`.tmp/goal-g448-ui-focused-verified.json`，首次记录`.tmp/goal-g448-ui-focused-final.json`保留。最终前端全量386通过、0失败/待定，`.tmp/goal-g448-ui-all.json`/log，本批新增十项。
- 最后TypeScript退出0，`.tmp/goal-g448-ui-types-final.log`；隔离Vite退出0、1.31秒，`.tmp/goal-g448-ui-build.log`，产物仅`.tmp/goal-g448-ui-dist`。最后全后端build和六包vet退出0，`.tmp/goal-g448-build-final.log`、`.tmp/goal-g448-vet-final.log`。Go新增指纹协议和接受事务测试不计为通过；SDK源码未动、没有重跑全量，最近1507通过仍标G4.47。所有进程已结束，diff检查通过。
- 原发布矩阵/脚本已只读检查，但未执行：其中会启动受限Go测试/演练服务和浏览器，并默认写旧证据路径，不能在现有限制下整体调用。后续核对其覆盖与同版证据，不将旧本地通过报告冒充G4.48完整验收。原多文件Skill测试确认注册/安装仍是backend夹具；父子任务测试确认子任务当前为只读隔离，不写共享工作区。这些限制继续明示，不扩大测试证明范围。
- 未push、部署、操作测试机/8860/8880/用户作品/数据库/网关/凭据或安装OCI、修改系统策略。待明确能力已发范围问题，不等回复才推进已授权代码。Goal保持active；本批不是最终review签署。

### G4.47 消息恢复、输入边界与后台恢复队列 review（2026-09-09）

- **发送失败丢草稿/附件且重试身份变化，已修复并组件回归**：Composer在服务确认前保留正文、Skill和精确文件引用，同一规范化内容、版本及上下文的手动重试复用原请求标识。未知结果后修改内容或引用需确认才生成新标识；明确的客户端拒绝允许重新提交。首页handoff失败也保留原内容和标识。请求等待期间锁定编辑，停止后迟到的响应不能清除新草稿；不是自动重发或修改SDK执行循环。
- **重试产生重复失败消息，已修复并组件回归**：Workbench缓存保存submissionKey，同键重试复用原乐观消息；旧请求的失败/完成只更新自身缓存，不覆盖后来的请求。完整App夹具验证503后重试仍使用同一API键且只有一个用户消息气泡。
- **Enter绕过上传/输入法合成，已修复并组件回归**：提交入口同时检查上传和当前请求，输入法isComposing及keyCode229不提交；上传完成后发送绑定实际返回的文件引用。既有视频批次仍需确认，未放宽材料类型或版本校验。
- **产物历史失败无反馈，已修复并组件回归**：历史按钮显示加载状态并防重复请求，读取失败显示可重试错误且保留当前正文；沿用G4.46按产物ID隔离迟到响应的行为。
- **后台恢复损坏任务堵住队首，源码已修复、行为待验**：ClaimAgentTask对明确checkpoint损坏/审批不一致，在同一事务内失败该任务、撤销未执行工具、写安全事件并保留checkpoint。下一次普通轮询可领取后续健康任务，不从头重放损坏执行。恢复读取补齐大小、schema/hash/version、所属attempt、审批列表和状态一致性校验；暂停attempt丢失checkpoint不能当作新任务启动。保留attempt失败标记阻止随后手动fresh retry，数据库或审计失败仍回滚。
- 新增Go测试文件`agent_task_resume_integrity_test.go`共十项场景：九种损坏/缺失/审批异常隔离及审计失败原子回滚。全部**未执行**；全后端build和六包vet退出0，`.tmp/goal-g447-build.log`、`.tmp/goal-g447-vet.log`。没有绕过Application Control，schema仍为63。
- 前端先复现八项失败：34项中26通过、8失败，`.tmp/goal-g447-ui-before.json`；首轮修复42通过，补完整App重试后43通过，`.tmp/goal-g447-ui-after.json`、`.tmp/goal-g447-ui-send-final.json`。新增用例合计九项，含Composer七项和工作台两项。首次全量376项中374通过、2失败，均为ProjectMaterialPicker旧断言漏了新增第六个请求标识参数；补完整参数断言后全量376通过、0失败/待定，`.tmp/goal-g447-ui-all-final.json`/log，原`.tmp/goal-g447-ui-all.json`/log保留。未删除失败用例或弱化材料引用断言。
- 本轮固定SDK全量1507通过，277.11秒，0失败/错误/跳过，`.tmp/goal-g447-sdk-all.xml`/log；仍为允许的隔离模型、临时文件及传输夹具，未运行真实Go/OCI集成。最后TypeScript退出0，`.tmp/goal-g447-ui-types-final.log`；隔离Vite构建退出0、1.42秒，`.tmp/goal-g447-ui-build.log`，仅输出`.tmp/goal-g447-ui-dist`。所有相关命令已取得终态，diff检查通过。
- 上述修复更新G4.46的三组待办，不新增产品方向。仍需原承诺跨模块覆盖收尾、完整Go行为回归及真实环境门禁；不称最终review或整个Goal已完成，不再给没有剩余工作估算支撑的小时数。没有push、部署、访问测试机/8860/8880、操作用户作品/数据库/网关或安装OCI，Goal保持active。

### G4.46 前端恢复、作品/产物状态隔离与规则重试 review（2026-09-09）

- **断线后状态不刷新，已复现并修复**：原轮询只读取running/pausing状态化Run，主对话、后台及等待审批均缺兜底。现在断线期间仅对仍活动的执行按3秒刷新完整作品projection，包含工具审批、输入收件和产物；同一轮询不重叠，SSE恢复或执行终结后停止，旧响应受既有sequence保护。不增加另一套执行控制或自动重放模型请求。
- **瞬时读取失败丢草稿，已修复**：已加载作品刷新失败不再卸载整个工作台，错误在现有页面显示并可重试；401/403/404仍撤下作品操作入口，不能将缓存当作当前授权。回归覆盖草稿保留、下次成功清除错误、撤权后不继续轮询。
- **切换作品/产物状态串用，已复现并修复**：路由以project ID隔离Workbench，防止前一作品失败消息被合入新作品；产物切换清除旧版本，Composer和正文只使用当前artifact ID匹配的版本，ArtifactWorkspace按artifact ID重建，丢弃前一产物迟到的历史列表。覆盖等待新正文期间不显示/编辑旧正文、不发送旧version ID，以及迟到历史不弹到新产物。
- **规则保存未知结果重试换请求身份，已复现并修复**：InstructionEditor保存规范化命令的request_id，同一保存或清空重试复用，修改命令才换ID；继续使用Go原有CAS和持久回执。模拟服务已提交但响应丢失，旧代码重试会因换ID而冲突；两个分支现均取回原版本回执，不新增版本。
- 新增`workbenchRecovery.test.tsx`14项，`AgentInstructions.test.tsx`增加3项。最初新增及既有规则测试共19项：7通过、12失败，保留`.tmp/goal-g446-ui-before.json`。首轮修复与事件/控制专项70通过，`.tmp/goal-g446-ui-after.json`。补撤权与迟到历史用例后前端全量47文件、367项通过，0失败/待定，`.tmp/goal-g446-ui-all.json`/log。全部为真实React组件/路由和jsdom事件配合API夹具，不是浏览器或真实服务E2E。
- 最后TypeScript检查退出0，`.tmp/goal-g446-ui-types-final.log`；Vite隔离构建退出0、2.42秒，`.tmp/goal-g446-ui-build.log`，仅输出至`.tmp/goal-g446-ui-dist`，未覆盖运行环境。后端/SDK源码未改，不重复宣称新跑1507项或Go行为测试；沿用G4.45固定代码证据。所有本批命令已取得终态，diff检查通过。
- **仍待修复/核验，不是全局review完成**：普通Composer发送前清空输入和附件，最终网络失败没有可用恢复动作；Enter路径未检查uploading/输入法合成；需统一失败保留及精确重试身份。产物历史读取失败目前从点击处理器逸出，需给可重试错误。后台`ClaimAgentTask`取第一条queued记录，恢复checkpoint报错时事务返回且记录仍queued，需核对并补损坏状态隔离/后续任务不饥饿，不能因为状态化队列已有防护就认为后台也具备。主对话checkpoint生产调用方只由内部manager传入当前generation，无本批新增绕过发现。
- 未启动浏览器或用户服务，未操作作品/数据库、测试机、8860/8880、网关、真实模型或OCI；未安装依赖、改系统策略、push或部署。继续原目标，以上明确剩余项与真实环境门禁分开，Goal保持active。

### G4.45 流式退出、Worker 生命周期及迁移完成标记 review（2026-09-09）

- **流式断连清理，已复现并修复**：取消事件消费时队列getter可能遗留；Starlette在发送响应头或事件失败时不会自动关闭暂停在yield处的生成器。`AgentExecutionStream`增加显式关闭并等待底层执行结束，队列等待在finally取消并回收；专用StreamingResponse在所有退出路径关闭生成器与执行，完成清理后才释放turn ID，避免同ID重入。使用AnyIO取消屏蔽保护既有清理，不另造模型循环。
- 新增`test_stream_shutdown.py`五项：队列取消及ASGI 2.0/2.4分别在响应头/事件发送处断连；检查清理未完成时同ID仍409，清理完成后可重新进入。原代码五项全部失败，14.26秒，`.tmp/goal-g445-stream-before.xml`；修复后与app/runtime专项合计26通过，5.96秒，`.tmp/goal-g445-stream-after.xml`。真实SDK执行包装及ASGI响应层配合受控operation夹具，无真实模型/Go/OCI。
- **Worker部分启动失败和关闭短路，已修复并回归**：lifespan将构造和启动包含在finally覆盖范围内，先登记再启动；一个stop失败仍等待其余Worker退出，清空列表后报告脱敏错误。`test_app_lifespan.py`三项覆盖中途构造失败、同app重新启动、AnyIO取消期间关闭及某stop失败时等待其他任务。扩展专项29通过，8.83秒，`.tmp/goal-g445-lifecycle.xml`。
- **迁移完成标记提前推进，源码已修复、行为待验**：`migrateRunConfigSnapshots`不再写全局schema版本；所有后续回填成功后由`completeRuntimeMigration`在最终事务内写成功回执与user_version。报告创建/写入失败会回滚该事务，清理本次未提交报告但不删除原备份。新增两种失败后重开/备份不变用例及单组件回填不推进版本用例，均未执行。schema仍63；不宣称全部迁移或文件系统与数据库组成原子事务，进程强杀时仍须以数据库提交状态为准。
- 显式声明已安装的AnyIO依赖，没有安装/升级依赖；固定SDK版本未变。最终SDK全量1507通过，259.55秒，0失败/错误/跳过，`.tmp/goal-g445-sdk-all.xml`/log。全后端build及六包vet退出0，`.tmp/goal-g445-build.log`、`.tmp/goal-g445-vet.log`。Go测试继续受Application Control限制，未重试或绕过；前端本批未改未重跑。
- 所需测试/构建命令已取得终态。未操作用户服务、作品、数据库或测试机，未push、部署、修改网关/系统策略或运行真实OCI。剩余恢复调用方、完整前端链与跨模块收尾继续按R1核对；真实环境门禁单列为V2。完整Goal保持active，本批不签署最终review通过，也不根据测试数量给完成比例或无依据交期。

### G4.44 父子任务生命周期联合与授权边界 review（2026-09-09）

- 新增 `test_native_subagent_lifecycle.py`，三个真实父流入口分别覆盖暂停和取消，使用固定SDK `Agent.as_tool` 并行子任务、真实临时文件及明确的模型/Runtime夹具。暂停先等待两个子任务结束，再保存原生工作区；重建后只由父Agent汇总已保存结果，不再次委派。取消等待两个子任务退出后关闭工作区，子任务没有共享父工作区或彼此覆盖产物。与既有子任务专项合计24通过，15.33秒，`.tmp/goal-g444-subagent-initial.xml`。
- **发现一，Skill生命周期撤权窗口，已修复源码**：普通上传/目录安装在包准备后复查权限，但持久化事务只有对话草稿分支继续授权；启停、切版本和卸载还在事务前切换活动目录。`skill_scope.go`复用既有`rowQueryer`/`resolvePrincipalQuery`建立事务内复查，安装、启停、切版本、卸载均在任何活动目录变更前检查当前角色/成员和项目存续；保留草稿自身更严格的审批/快照检查与原文件回滚。
- **发现二，项目文件事务信任旧用户身份，已修复源码**：`projectFilesWorkspace`现于调用方事务内读取当前用户/工作区/成员状态，写入同时保留请求身份和当前角色的editor门禁。降级viewer仍可读取，撤销成员或禁用用户/工作区后不能列出、读取、下载或继续写文件。没有扩展文件路径权限，也未改变无显式身份的既有受信内部调用约定。
- **发现三，后台Worker令牌只在HTTP前置认证检查，已修复源码**：`validateAgentTaskToolCallTx`此前只检查任务状态/租约，状态化分支则已有事务内令牌校验。后台分支现核对当前attempt、项目、单一执行模式及确切令牌；显式service调用缺少activity时拒绝。覆盖现有工具启动/完成、文件写入等调用此函数的路径，不放宽终态收尾或旧结果幂等语义。
- 新增`skill_mutation_authorization_test.go`及文件回归源码，覆盖三个安装范围在预检查后撤权/删除项目、拒绝前不更换活动文件、不新增版本/事件，以及用户撤权和Worker令牌轮换后旧请求拒绝、新Worker仍可提交。源码复核时修正了fixture角色/成员状态为实际schema的viewer/disabled；**所有这些Go行为用例未执行**，不能算测试通过。
- 本批还读取了`mcp_connections.go`完整更新/解析链、内部HTTP身份绑定、原生快照发布、规则事务/回执、外部写入成功门禁，以及MCPConnections/ProjectFiles/AgentToolOutcomeReview前端组件。MCP凭据已有事务内当前身份检查、AES-GCM绑定及no-store；发布已有快照/审批/租约/CAS和整批事务检查。未据局部阅读宣称这些模块没有任何风险；其他执行恢复入口、启动/迁移和完整前端链待继续审查，未执行浏览器。
- 最后版本SDK全量1499通过，373.64秒，0失败/错误/跳过，`.tmp/goal-g444-sdk-all.xml`/log。Go全后端build和六包vet均退出0，最终证据`.tmp/goal-g444-review-build.log`、`.tmp/goal-g444-review-vet.log`。Go测试仍受Application Control限制，未重试测试程序或更换路径；SDK成功不能替代Go事务回归。前端未改，沿用G4.43的350项及构建证据，不声称本批又执行了UI测试。
- 所需命令均已取得终态，diff检查通过。schema仍63，未push、部署、操作用户服务/作品/数据库、网关、真实模型或OCI，也未修改系统策略。全局review正在进行但未完成，V1/V2真实验收仍保留，完整Goal保持active；没有新的无依据交期承诺。

### G4.43 原生暂停恢复、输出修复和多文件 Skill 用户任务验证（2026-09-09）

- 新增 `test_native_workflow_boundaries.py`，使用三个实际执行流入口和固定SDK，批准原生补丁后实际写入临时文件。在用户主动暂停或模型临时故障边界序列化，再重建owner、追加要求并恢复。验证原文件只写一次、原审批不重放、追加要求只进入模型一次、收件记录与最终关闭一致。模型与Runtime为夹具，没有真实模型/Go/OCI参与。
- 状态化输出修复分别覆盖原owner和重新装配owner，保留工作区及前阶段用量；修复模型没有文件/终端工具，只修复原候选，不重新生成或执行先前工具。首次8项有6项因新测试多写一个末尾换行预期失败，已对照既有固定SDK补丁断言修正为原字节，未改生产文件语义；第二轮8通过，13.75秒，`.tmp/goal-g443-boundaries-second.xml`。
- 新增 `test_native_skill_authoring.py`，三模式由SDK原生补丁创建SKILL.md、references/checklist.md和assets/example.txt，经补丁、发布、安装三次原生审批及owner重建，按精确快照发布，随后校验、安装、load和读取新版本资源。全部三项通过，14.19秒，`.tmp/goal-g443-author-initial.xml`。检查三个文件各写一次、发布/安装各一次、资源内容和版本正确；未在宿主Skill目录注册。
- 新安装Skill在当前轮通过已有注册表资源工具直接使用，不声称初始冻结的`.skills`目录已被重写；沿用现有按需指令/资源加载，不增加另一套物化框架。本批注册表校验/安装为明确标注的后端夹具，真实Go包校验/注册、用户页面和外部模型仍待联合验收。
- 本批没有新增产品功能或变更生产逻辑，新增11项联合测试证明已有G4.42接线。包含新增用例的SDK全量1493通过，333.48秒，0失败/错误/跳过，`.tmp/goal-g443-sdk-all.xml`/log。前端全量350通过、0失败/待定，`.tmp/goal-g443-ui-all.json`；TypeScript退出0；Vite隔离构建退出0、10.64秒，只输出到已核验的`.tmp/goal-g443-ui-dist`，没有清空或覆盖用户发布目录。
- 本轮所需测试/构建进程均已取得退出码，diff检查通过。真实Go/OCI、网关、浏览器及回滚验收未执行，不把分层模拟回归拼成完整真实E2E。未部署、操作用户服务/作品/数据库、安装OCI或修改策略；最终全局review和完整Goal尚未完成。

### G4.42 生产入口外部 Session 所有权与权威终端恢复（2026-09-09）

- 主对话、后台Worker、状态化Worker三个生产源码入口均使用公开 `SandboxRunConfig(session=...)` 和 `RuntimeSandboxPTYSession`。审批/用户暂停/可恢复模型故障时保存快照但不隐式关闭活环境；终态、取消和提交前显式确认清理。仍保持原默认关闭配置，没有切换用户服务。
- 新增版本化 `NativeWorkspaceCheckpoint`，平台context只序列化会话、环境、ready/closed和精确快照引用，不保存租约密钥、任意进程权限或宿主路径；三个恢复入口严格解析。Runtime新增只读PTY目录接口，核验执行身份、当前租约、环境及确切进程序列，没有引擎I/O副作用。Go schema仍为63。
- 恢复ready环境直接重绑定Runtime已登记的原进程，并在启动/使用前复查目录一致性；不将较旧快照覆盖到仍活着的工作树。确认环境已关闭时只能恢复最后保存文件，旧终端明确lost，不能自动启动或重放stdin。管理员原有进程时限与租约未放宽；暂停快照之后未保存的工作也不被宣称可恢复。
- 旧SDK-owned文件checkpoint只在backend、主session及各Agent共享session引用全部与Runtime确切一致且原环境closed时迁移。缺失、篡改、旧快照、跨环境或关闭后反向变活均拒绝；关闭原生功能时，含原生checkpoint的执行不能静默降级为无工作区。
- 三模式跨两次审批/owner重建覆盖批准、拒绝和环境丢失；真实随机loopback严格验证PTY目录/传输，异常载荷、重定向和未知响应不自动重试。所有引擎/终端操作仍为夹具，不是Go/OCI实际进程恢复证明。
- 首轮专项72通过10失败，其中9项为旧测试“每次审批都关闭环境”的预期，1项把最新context覆盖到旧RunState而丢弃了待验证的应用checkpoint。更新为新的外部session语义、恢复原保存context后95通过39.66秒；扩大专项527通过110.62秒；增加旧版本兼容及禁用恢复后17通过20.93秒。
- Go全后端build及六包vet通过，证据`.tmp/goal-g442-build.log`和`.tmp/goal-g442-vet.log`。新增三模式权威目录/错误状态/租约/HTTP身份测试源码未执行，不计行为通过。完整SDK原日志因中断止于14%，原进程退出后重新完整执行1482通过，384.37秒，0失败/错误/跳过，见`.tmp/goal-g442-sdk-all-resumed.xml`/log；G4.40 MCP偶发失败历史不抹去。

本批替代G4.41“生产仍用非PTY provider”的历史状态，推进D2源码接线而不宣称用户部署已更新。未push、部署、访问测试机或用户环境、改网关/凭据、安装OCI或修改系统安全策略。原D3/V1/V2/R1完整门禁继续保留，Goal保持active。

### G4.41 SDK 终端会话、确认终止及共享 I/O 队列（2026-09-09）

- `NativeWorkspaceManager.TerminatePTYs` 复用环境操作锁和现有进程账本，不增加迁移或第二套任务系统。仅终止当前执行/环境登记的进程，信号前将选中进程标为未知，全部确认退出后才记为 exited 并保留文件供快照使用；历史工具回执保持原义。部分成功、超时、撤权、租约失效或回执未知均不伪报保存成功，也不自动重放信号。缺失或已确认关闭的环境不隐式创建资源。
- 新内部 `POST .../pty/terminate` 在读取请求体前检查身份及租约，只接受有界空对象，不接受宿主 PID、命令或信号。Python 严格核验当前 session 和 stopped 回执，不重试、不跟随重定向；保留 4KiB 请求/64KiB 响应限制，仅确切终止端点给原有120秒环境操作和15秒收尾留出145秒客户端窗口，没有延长命令时限、MCP超时或资源策略。
- `RuntimeSandboxPTYSession` 实现固定 SDK 的 `pty_exec_start`、`pty_write_stdin`、`pty_terminate_all`；使用 SDK 命令准备、轮询等待归一化和输出 token 截断。每次输入沿用原始审批调用与确切进程序列。`write_stdin` 接入与 exec 相同的输入策略、必须确认的开始/完成/失败审计，不开放无审批输入或自动重试。
- 未确认 PTY I/O 的 pending 标记保留在 SDK state，阻止继续读写、快照、自动清理及未经核验重开。活跃终端标记与文件快照分开；已发布文件不能被误当作可恢复进程。SDK stop 先确认终止再导出/保存，未知终止结果不会被 SDK 后续清理掩盖为成功。
- 顺着接线发现同一 SDK 会话的文件和终端使用独立锁，并发时会竞争 Runtime 环境操作。文件、单次命令、终端和导出现在共用会话 I/O 锁；发布等待进行中的 I/O 后，在锁内复查回执再读取快照。三种先后顺序的并发测试验证排队而非重复执行或合法请求互相报错。
- 实际 SDK Runner + 模拟终端验证通过公开 `SandboxRunConfig(session=...)` 在启动审批、输入审批批准/拒绝之间保持同一个会话，最后由所有者显式关闭。另覆盖二进制输出、SDK 截断、非交互进程只轮询不接受输入、未知回执/取消/终止不重放、文件快照不是活进程、保护值输入及真实随机 loopback 的三模式身份传输。没有真实模型、Go权限或OCI进程参与，不能算用户链路或真实PTY验收。
- 首轮33项中30通过、3失败，源于新HTTP测试替身缺少标准响应头/URL方法；完善替身后相关回归156项通过（35.94秒）。补活进程标记后原生专项450项通过（62.63秒）。共享队列首轮453项中450通过、3失败，源于测试计数未扣除SDK初始化mkdir；按初始化基线计数后重跑同版完整专项，453项通过（70.52秒），0失败/错误/跳过，证据 `.tmp/goal-g441-native-verified.xml` 及同名log。不是重跑到偶然通过，也未删除失败断言或改变真实超时。
- Go新增三模式终止后导出、历史回执、未知结果、过期租约、并发操作、空环境不创建及HTTP拒绝任意进程输入/身份先于body等测试源码。全后端build与六包vet通过，见 `.tmp/goal-g441-build.log` / `.tmp/goal-g441-vet.log`；仍未运行被Application Control限制的Go测试，不将源码或静态检查计为行为通过。全量diff检查通过。
- **仍未接入生产入口**：`native_runner.py` 的三个入口继续用 `RuntimeSandboxFileSession`，不提供PTY或write_stdin；新PTY provider的支持只在隔离绑定中启用。后续需同时切换公开SDK外部session所有权、平台恢复引用持久化和Runtime活进程重绑定。不能仅保留内存对象、不能依赖外部session模式下不存在的SDK sandbox序列化、不能把旧快照覆盖到活进程工作树。进程实际到期/丢失仍必须明示，不能自动重启。

本批推进D2并修复原生工作区并发，未完成D2/D3/V1/V2/R1；最终全局review尚未开始。最近全量SDK的两项MCP连接失败继续保留为V1问题。本批未push、部署、操作测试机、8860/8880、用户作品/数据库、真实模型或容器，也未安装OCI、修改网关/凭据或系统策略。Goal保持active，不报完成比例或新的无依据交期。

### G4.40 PTY 审批回执接线与发布/Skill 恢复修复（2026-09-09）

- `native_workspace_pty.go` 与源码 schema 63 增加进程和操作账本。启动/输入先核验当前执行、租约、策略、原始 SDK 调用及已消费审批，再记录意图并预留回执容量，最后调用 G4.39 引擎。输入还复查原启动审批，绑定确切字符、进程和期望序列。没有另写模型循环。
- 完成状态与原子回执分开：工具可以已完成而进程仍在运行。相同调用返回哈希绑定的历史原字节，不重复启动或输入；运行中/失败/丢失回执不自动重放。单次 Shell 与 PTY 不能复用同一个调用重复执行。历史 running 回执不是进程当前仍活着的承诺。
- 每会话最多128个进程、512次I/O、128MiB回执；每次先预留2MiB，同时计入既有工作区存储配额。收尾复查撤权/取消，确认环境删除后标记活跃进程 lost；项目删除清除输出及预留。新迁移沿用备份机制，但未对用户数据库执行。
- 内部 `POST /internal/v1/native-workspaces/{session_id}/pty` 在读取输入前校验执行身份和租约。Python传输验证请求/结果哈希、进程序列、规范base64、输出原字节、退出状态和实际输入字节，未知结果/重定向不重试。校验放在JSON复制前，避免非法UTF-8被替换或过大请求先分配内存；不存在的终端返回明确session lost。
- 发现G4.38发布传输误用普通控制接口4KiB请求/64KiB响应限额；仅确切发布端点改为256KiB/512KiB，未放宽其他控制请求。256文件用例真实经过随机loopback HTTP并越过原限额；不等于真实Go发布或Skill安装验收。
- 传输/发布专项128项通过，44.03秒，证据`.tmp/goal-g440-transport.xml`。新增Go测试源码覆盖三模式审批、输入、重开DB、未知结果不重放、互斥执行、撤权/取消、删除、配额、UTF-8、HTTP身份先于body及v62升级；当前全后端build和六包vet通过，证据`goal-g440-build-final.log` / `goal-g440-vet-final.log`。受Application Control限制，Go测试源码未执行，不能计为通过。
- SDK首次全量1398项中1396通过、2失败，506.46秒，证据`.tmp/goal-g440-sdk-all.xml`。一项明确为MCP stdio连接10秒超时；另一项有连接失败日志，随后新安装依赖未注册导致固定测试模型调用不存在的工具。保持原配置/超时的两个模块复验22项通过，60.88秒，见`goal-g440-mcp-recheck.xml`；复验不抹去首次全量失败，也未证明连接性能问题消失。
- 顺着该失败确认并修复现有安装后激活恢复缺口：安装已提交但依赖连接失败时，将确切验证过的新版本保留为本轮可发现、待激活状态，返回ready=false。后续显式load复用已有发现/激活机制，不再要求重装。升级失败期间保留旧权限/版本，显式load成功才替换；冻结元数据变化仍拒绝，不扩展未批准的目录或自动执行依赖动作。
- 新建故障注入覆盖真实主Runner安装、load、依赖审批批准/拒绝和恢复；相关专项153项通过，104.90秒，见`goal-g440-final-focused.xml`。其后补三模式升级与篡改元数据共6项通过，17.42秒，见`goal-g440-upgrade.xml`。这些均为隔离模型/后端夹具。
- 修复后末版SDK全量1406项中1404通过、2失败，444.02秒，0错误/跳过，证据`.tmp/goal-g440-sdk-all-final.xml`及同名log。8项新增安装/升级恢复用例均通过。剩余失败为`test_main_runner_discovers_unrouted_skill_and_resumes_its_native_approval[reject]`与`test_background_author_install_load_and_second_approval_survive_two_worker_restarts[mcp]`：均有MCP连接失败，后台用例随后固定模型仍调用未装配工具而进入失败。安装后恢复修复不代表MCP连接稳定性已解决；不提高超时或改真实配置来凑绿，不继续全量重跑直到偶然通过。这两项纳入V1继续定位。
- **仍未接线**：SDK `pty_exec_start` / `pty_write_stdin` / `pty_terminate_all`、单独生命周期终止接口和审批暂停的活进程恢复。SDK现有session在Runner返回时关闭的问题仍存在，supports_pty仍为false；不得提前开放write_stdin或把目录描述符算成可用功能。后续使用公开session配置处理owner，不把文件快照当进程镜像，不放宽审批与过期规则。

本批推进D2并修复D3的实际缺陷，不代表补齐、自测或最终全局review完成。未push、部署、操作测试机、8860/8880、用户作品/数据库、真实模型或容器，也未安装OCI或变更系统策略。Goal保持active；D3剩余矩阵、V1/V2/R1继续保留。

### G4.39 受限交互式终端执行传输（2026-09-09）

- 上一个 Goal turn 仅核对并报告状态，没有代码推进，按 no progress 处理；本轮继续现有 D2 缺口，未将外部验收条件当成停止实现的理由。
- `scriptsandbox/workspace_pty.go` 增加 StartWorkspacePTY / WriteWorkspacePTY，复用现有精确容器身份校验、工作区操作锁、固定 UID/环境和 OCI 删除。进程 ID 不是宿主 PID，绑定完整 workspace handle；同一引擎内已使用的 ID 不再启动，重建引擎后输入返回 session lost，不从文件快照猜测恢复进程。
- `workspace_pty.py` 是 Go 内嵌且仅在受限容器中通过 `python -I -B -u -c` 执行的固定桥接器，复用 Python 标准库 PTY/进程/选择器。没有将 SDK 的模型循环或 Shell 参数解析另写一套；不写宿主目录、不安装依赖、不放宽 noexec，不使用需要宿主 TTY 的 docker exec -t。TTY 为固定80x24，非TTY关闭stdin。
- 启动帧在引擎操作前检查，后续帧严格顺序、单次请求、规范base64及大小。单次输入64KiB、输出1MiB，总命令时限沿用管理员1至300秒（默认30秒）；轮询不延长总时限。记录实际写入字节，部分写入/提前退出不伪报全部写入。空闲时仍收集有界输出并检查退出和时限。
- 终止先向未回收的自有进程组和子进程发送信号，再等待并回收，避免PID复用与刚fork尚未setsid的竞争。未确认的传输/关闭不自动重放，而是沿用精确容器删除；删除失败仍报 cleanup unconfirmed。工作区删除关闭自有流；终态释放流缓冲但保留有界防重启标记。每工作区同时进程数受pids配额且最多16，单引擎最多128活跃PTY、4096身份记录；不是绕过原资源策略。
- 55项新PTY协议测试只使用进程/信号/时间/通道替身，没有在Windows宿主执行PTY或发送信号。首轮51项通过，超长参数自动生成的用例名触发Windows环境变量长度限制并产生setup/teardown两项错误；改为短id后通过。终端加原文件辅助协议143项通过，最终扩展至现有SDK能力装配和三个Runner入口共185项通过，19.81秒，0失败/错误/跳过，证据`.tmp/goal-g439-native-focused.xml`及同名log。
- Go新增传输身份、二进制输入输出、取消/退出、未知回执、不重启丢失进程、限额及大帧分块测试源码；另写真实OCI TTY、Ctrl-C、stdin EOF和退出后快照用例。全后端build及六包vet通过，证据`.tmp/goal-g439-build-final.log` / `goal-g439-vet-final.log`。受既有Application Control限制没有运行任何Go测试；真实PTY/POSIX ABI/OCI用例没有运行，不计作通过。
- **仍未接线**：Runtime必须先持久记录进程/请求身份、精确审批及不可重放回执，再调用上述引擎；HTTP和SDK `pty_exec_start` / `pty_write_stdin` / `pty_terminate_all` 尚未接入。`supports_pty()`仍不开放，不新增虚假的Agent工具入口或变更approval=always。当前SDK管理的session在Runner返回时stop/shutdown，stop会终止PTY；下一步需以公开session配置解决审批暂停的生命周期，而非伪装关闭成功或把快照当进程镜像。保留真实进程过期/丢失的明确结果。

本批推进 D2 的实际执行层，不代表 D2 完成，更不代表补齐、自测、全局review完成。D3/V1/V2/R1继续保留。未push、部署、操作测试机、8860/8880、用户作品/数据库、真实模型或容器，也未安装OCI或变更系统策略；Goal保持active。

### G4.38 原生文件发布接工作文件与 Skill 草稿（2026-09-09）

- 复用现有项目版本文件和 Skill 草稿校验、ZIP、审批安装链，不增加第二套文件管理或 Skill 注册机制。源码 schema 62 支持二进制原字节和单次调用的多个路径；旧文本字段序列化保留，迁移备份沿用 Store。未迁移用户数据库。
- 新增 prepare_workspace_publication 和 publish_workspace_files，接主对话及后台/状态化统一工具清单，仅有原生工作区时启用。准备通过 SDK snapshot.persist 保存确切快照但不终止会话；发布固定 approval=always/max_retries=0，绑定 snapshot version/hash、源路径、目标路径及 expected_version。普通旧文本工具保持原行为。
- Go 发布接口检查当前身份/租约/策略、审计调用及已消费审批、快照完整性、路径冲突和聚合配额，在一个事务写所有目标及事件；仅使用存储快照，不调用引擎。相同调用回执保持原顺序和历史版本，不能覆盖新编辑；缺失/改变/超额时拒绝整批。目标禁止使用保留的 .skills 命名空间。
- 工作文件下载返回确切版本原字节，保留 attachment/nosniff/private no-store。前端二进制文件显示元数据和下载，不伪装为空文本，不提供字符翻页或二进制 SKILL.md 的校验入口。作品页监听新的发布工具完成，自动刷新现有文件列表。标准 Skill 草稿打包保留二进制资源字节。
- 修复本轮发现的既有路径校验返回值漏查及工作区初始化未排除删除文件的问题。原生指令接线初版把字符串改成函数，破坏状态化/Skill handoff 的既有约定，已修复为保留原类型。首次最终全量的两项失败来自旧测试断言假设所有工具默认可见，已明确校验原生开关关闭时不提供发布工具，并重跑。
- 隔离专项86项通过，包含三模式冻结、审批拒绝/批准、SDK RunState重建、准备后修改仍发布旧快照、二进制回执、未知保存不重试、错误回执拒绝和旧状态化审批回归；另覆盖真实随机loopback HTTP身份传输。模型/Go权限/引擎为夹具，文件与SDK是真实执行，不能视作Go/OCI联合验证。证据`.tmp/goal-g438-publication-final.xml`。
- 前端全量350项通过，0失败/跳过，报告`.tmp/goal-g438-ui-all.json`；TypeScript原项目构建检查通过。首次尝试在tsc -b附加tsBuildInfoFile为无效选项，改回项目原命令后通过；Vite构建仅输出到`.tmp/goal-g438-ui-build`，未覆盖用户正在使用的构建或启动服务。
- Go新增三模式发布/草稿安装/二进制资源、旧回执不覆盖新版本、原子拒绝、v61迁移兼容、HTTP原字节下载及身份校验测试源码；全后端build和六包vet通过，仍未运行Go测试。SDK最终全量1289项通过，360.98秒，0失败/错误/跳过，证据`.tmp/goal-g438-sdk-all-final.xml`及同名log；不是整个Go/SDK/浏览器平台的最终验收。diff检查通过，所有测试命令均已退出。

仍需D2交互式PTY、D3原生追加/修复与完整用户链联合、V1同版Go/三模式全量、V2真实环境和回滚验收及最终R1全局review。功能接线不等于验收完成。Goal保持active；未push、部署、操作测试机、8860/8880、用户作品/数据库、真实模型/OCI或系统安全策略。

### G4.37 三执行入口接入原生工作区（2026-09-09）

- 贯通主对话的dispatch generation传输，并在Sidecar增加与服务器同名、默认关闭的显式开关。主对话、后台Skill、状态化Worker分别绑定SandboxAgent及原Files/Shell/Skills能力，保留已有SDK Session、guardrails、审批和工具目录；非执行路由和仅输出修复不自行获得新环境。
- 原生工作区owner只在执行内存中保存传输/租约，排除于SDK应用context序列化。一个执行连续多次Runner调用保留同一租约与最新确认快照；新进程通过Runtime恢复接口取得封存manifest和最新快照精确引用，再由SDK恢复会话。不把checkpoint自带的清单作为它自身的授权依据，也不接受陈旧快照覆盖当前版本。
- Skill handoff保留SandboxAgent类型，允许同一Runner内多个Agent共享同一受限会话；不同清单、不同恢复引用、已关闭会话不能借共享模式新建或替换工作区。默认客户端仍拒绝第二所有者。
- 主对话commit_agent_action在真正调用Go终态提交前执行SDK会话清理、确认快照与关闭，再暂停续租，避免续租与已提交终态竞争；提交失败恢复原owner的守护。后台/状态化返回结果前也检查保存关闭。流式取消等待实际Runner任务结束；保存和关闭各自串行、成功幂等，未知结果不由后续SDK清理重复发送。保留SDK pre-stop hooks。
- 三个实际流式执行方法的20项专项通过：连续两次Runner保持文件、保存/关闭失败拒交付、主对话提交先后顺序、Skill handoff共享、拒绝陈旧恢复、三模式取消、三模式审批重建后只执行一次命令。使用固定SDK、真实临时文件与模拟模型；Go权限/租约/引擎仍是夹具，不能称为Go/OCI联合验收。
- 新增18项实际随机loopback HTTP恢复回执测试、8项开关解析测试和1项续租暂停/恢复归属测试。Go新增三模式恢复引用/完整性测试及HTTP鉴权/引用测试源码，仅通过vet，未运行被系统策略阻止的测试程序。
- 同版完整Sidecar回归1249项通过，221.44秒，0失败/错误/跳过，证据`.tmp/goal-g437-sdk-all.xml`与同名log；20项入口专项见`.tmp/goal-g437-runner-v5.xml`。Go全后端build、六包vet及diff检查通过；不将build/vet算作Go行为回归。

仍未完成D3文件发布/二进制/安装UI闭环、D2交互式PTY、原生环境下完整追加及修复联合矩阵、V1同版Go/UI/三模式全量、V2真实条件验收及最终全局review。下一步继续这些原范围缺口，不重复已接的基础owner。Goal保持active；未push、部署、操作测试机、8860/8880、用户作品数据库、真实模型或容器，也未变更安全策略。

### G1 工具延迟加载

- 代码修复完成：AgentToolProvider 在权限与启用过滤后，只要实际提供的 Function Tool 启用延迟加载，就添加原生 ToolSearchTool；Hosted 包装共用这一逻辑。不启用的工具、只读模式排除的写入工具不会通过搜索重新出现。
- 新增 6 个参数化测试用例，覆盖普通/Hosted 工具、延迟开关、禁用和只读权限过滤。工具模块 14 项通过；扩展到 Hosted、RunState 审批、后台审批和 guardrails 的针对性回归共 33 项通过。
- 首次受限运行的 stdio MCP 测试遇到 Windows 命名管道 WinError 5；经执行权限审查后，在本机重跑通过。没有绕过应用权限或调用真实模型，未修改运行中的服务。
- 网关真实 ToolSearch 调用仍待验收；不能用本地契约通过代替真实模型工具发现。

验证命令：本地 `.tools/openai-agents-sidecar-venv/Scripts/python.exe -m pytest`，目标为 `test_agent_tools.py`、`test_hosted_tools.py`、`test_run_state_approval.py`、`test_background_approval.py`、`test_sdk_guardrails.py`，设置禁写 pyc 和关闭 tracing，pytest 缓存关闭，临时目录 `.tmp/goal-g1-integration`。退出码 0，`33 passed in 4.37s`。

### G2 原生工作区与 Skill 完整链路

- 已读取固定 SDK 的 SandboxAgent、Docker provider、session/client 接口，以及平台现有 scriptsandbox 与 Skill 安装/升级实现。
- 确认现有 Go scriptsandbox 接口为受限单次脚本执行，不提供等价的通用持久 workspace/session；不能在未验证恢复、权限、配额前把两者直接替换。
- G2.1 通用文本工作文件基础层已实现并完成针对性验证：Go schema 39 增加项目内版本文件，主对话与后台 Agent 接入 list_workspace_files/read_workspace_file/apply_workspace_patch。补丁算法直接复用固定 SDK 的 agents.apply_diff.apply_diff；Go 只负责业务数据存储、版本 CAS、配额、权限和调用审计，不解析补丁或运行文件脚本。
- 写入绑定 SDK 调用 ID、规范化参数哈希、正在运行的调用与 turn/task 生命周期；同一调用幂等，并发过期版本、跨租户、路径穿越、大小写/父子路径冲突、已取消执行均拒绝。文件写入有事件审计；删除项目/工作区清理文件，删除预览纳入文件版本变化，旧预览不能删除之后新建的文件。
- 前端工作台增加“工作文件”入口，可浏览、分页预览、选择历史版本与下载精确版本，错误时不提供未经验证的下载。弹窗修复了 React StrictMode effect 清理触发迟到 close 事件导致自动关闭的问题，补充测试覆盖。项目删除确认展示工作文件及历史版本删除数量。
- 当前存储限制：UTF-8 文本单文件 1 MiB，工具参数仍遵守既有 256 KiB 限制，项目 256 个路径、4096 个版本、64 MiB 历史内容；历史内容计入工作区存储配额。文件是数据库中的项目逻辑路径，不是宿主机路径；二进制文件和真实 SandboxAgent/Filesystem/Shell 会话尚未接入。不得把此层称为完整 SDK Sandbox。
- G2.1 结束时仅能写 SKILL.md 及文本引用文件，尚缺校验、确认安装与注册调用；后续 G2.2 实现与证据见下。普通 Artifact 导出、完整沙箱及真实 Agent 验收不能由本文件层替代。

G2.1 验证记录（不代表阶段 2 的全量真实 Agent 验收）：

- Go：Runtime 全包通过，287.334 秒；HTTP API 全包通过，68.097 秒；agenttool 全包通过，2.095 秒。首次 Runtime 全包命令的 3 分钟总超时不足，按仓库默认 10 分钟重跑通过；新增 3 个工具导致旧目录数量断言失败，更新为 23 并显式校验新增工具权限。
- Go 补充回归：目录父子冲突、v38 到 v39 备份迁移、工作区删除、后台取消等最后补充后，TestProjectFile* 在 runtime/httpapi 再次通过（10.721 秒 / 4.230 秒）。未操作运行中的数据库。
- Python：工作文件、真实本地 HTTP 客户端身份传递、主执行器、后台 Worker 与后台审批，94 passed in 4.94s；含 Provider 成功审计及补丁失败记录为 failed 后返回 SDK 可恢复错误，避免语法错误直接中止整轮。临时目录 `.tmp/goal-g2-integrated-files-final2`。
- 前端：ProjectFiles、AgentExecutionHistory、runControls 共 30 项通过，TypeScript 构建检查通过。
- Playwright：独立 8893 前端加本地 HTTP fixture，1440x900 / 390x844 均通过文件内容、长路径无溢出、精确版本下载、下载内容校验、历史切换、Escape/焦点恢复。截图与报告在 `.tmp/goal-g2-files-ui/`；明确为测试业务数据，不是模型或真实项目 E2E。浏览器启动经权限审查；未连接用户作品 API。
- 本批 UI fixture 进程 PID 42448 已在核对命令行测试标记后关闭；没有可供真实作品使用的新 URL，也没有切换 8860/8880。文件工作继续使用源码与隔离测试，完整功能后另行启动真实隔离验收环境。

### G2.2 Skill 草稿校验、安装与调用接线

- 源码 schema 40 增加 `project_skill_install_receipts`。`project_skill_draft.go` 将项目版本文件打包，复用既有 ZIP 校验、不可变版本、安装范围、升级和注册实现；没有另造 Skill 解析器或模型执行循环。校验、导出不安装也不执行脚本。
- 主对话和后台工具清单增加 `validate_workspace_skill`、`install_workspace_skill`。安装工具固定写权限和 SDK approval=always；通过现有 SDK RunState 暂停/序列化/批准恢复机制执行，不把聊天中的“确认”当作审批记录。
- 校验返回项目、目录、文件版本快照哈希、包信息与可见升级目标。安装参数绑定快照、范围、安装 ID、预期活动版本；事务内复查委托用户成员权限、SDK 参数哈希、获批调用及运行中 turn/task。草稿改变、升级竞争、范围不符、取消或权限撤销均拒绝；审计记录委托用户。
- 持久回执与安装版本在同一事务提交。同一请求重试或并发升级返回同一回执，旧请求不会激活旧版本；数据库重开后仍幂等。已卸载的同名 Skill 可由新的确认重新安装，旧回执重放不会擅自恢复它。
- 修复本批测试发现的冻结目录问题：只把当前执行批准安装的精确版本合入其目录，不修改其他 turn 或兄弟 task attempt 的快照；按事务插入顺序处理多次安装，不依赖可能回拨的宿主时间。无新增脚本/MCP 依赖的 inline Skill 经目录版本、哈希和 scope 验证后可在当前对话读取指令/资源。升级后使用新的已验证绑定，不被原请求旧版本覆盖。
- 补齐后台作者安装后的再次审批恢复：后台开放指令加载工具，恢复时重新核验新增 inline Skill 的目录与元数据，再准备工具；后台任务自身的主 Skill 版本不可被安装动作替换。模拟模型已验证安装审批、读取新 Skill、再次审批、两次重建 Worker 后完成，安装只执行一次；这不是实际进程/网关/数据库联合 E2E。
- 前端在工作文件的当前 SKILL.md 版本提供“校验 Skill”。面板显示校验结果、版本、模式、文件数、脚本/依赖，提供快照绑定 ZIP、权限允许的范围以及显式安装/升级确认。网络不确定重试复用幂等键，草稿/版本冲突要求重新校验；安装后刷新作品能力目录。历史/删除版本不冒充当前可安装草稿。
- 边界仍明确：本批没有真实模型或用户作品 E2E；含脚本/MCP 依赖及后台/状态化模式不会被宣称当前轮即时可用，仍需后续工具准备与跨模式验收。工作文件仍为受限文本业务存储，不是原生 SandboxAgent 的完整文件系统；二进制、Shell、普通 Artifact 文件交付仍未补齐。GAP-01/02/08 等保持未勾选。

G2.2 验证记录（隔离本地，不代表阶段 2 全量真实验收）：

- Go Runtime 全包通过，357.174 秒；agenttool 全包通过，0.558 秒。HTTP API 全包首次在 `TestRuntimeHTTPProjectDeleteAndAssetParseRetry` 的临时资产文件清理阶段出现 Windows Access denied，业务断言未失败；单独全包重跑通过，43.302 秒，保留该偶发异常记录。
- 最后补充升级版本绑定、时钟回拨、8 个并发同键升级、事务前权限撤销后，`TestProjectSkillDraft*` 在 Runtime / HTTP API 通过，14.257 秒 / 3.777 秒；Skill 快照/版本/选择相关扩展回归通过，28.125 秒。HTTP 覆盖租户、只读角色、ZIP 响应、缺确认/幂等键、SDK 审批和取消后的拒绝。
- v39 到当前 v40 的专门迁移测试通过，4.510 秒：保留工作文件与快照、生成备份记录、新建回执表后可安装；仅在临时数据库执行，未迁移运行环境。
- Python 最终相关回归 136 passed in 6.49s，包含作者工具、SDK RunState 批准/拒绝与恢复、工作文件、主执行器、后台 Worker/两次审批恢复、Skill 资源、真实本地 HTTP 客户端的 URL/参数及身份传递；临时目录 `.tmp/goal-g2-skills-final-with-background`。模型为本地 SequenceModel fixture，无真实模型请求。
- 前端全量 29 文件、182 项测试通过；TypeScript 构建检查通过。新增确认、升级范围、同键重试、冲突失效、只读/非法包和重复点击测试。
- Playwright 经权限审查启动隔离 Edge：1440x900 / 390x844 的校验面板、长版本号换行、安装参数和确认、原文件精确下载、历史切换、Escape/焦点恢复均通过。截图/报告 `.tmp/goal-g2-files-ui/skill-draft-1440.png`、`skill-draft-390.png`、`report.json`，已查看截图并修正主按钮样式；报告明确为 mocked-api UI，不是模型或真实项目验收。
- 本批隔离前端 8893 的 PID 13880 已核对命令行后停止，测试进程均退出；未启动或迁移用户环境，未 push、部署或操作测试机。源码 schema 40 不代表运行中的 8860 已升级。

### G2.3 普通 Artifact 精确版本下载

- `artifact_delivery.go` 为已保存的 `generic_document` / `generic_table` 提供只读下载回执与内容端点，绑定不可变版本 ID，重新校验作品、租户和 Agent activity 项目；不经过剧本候选确认/导出流程，不产生额外导出记录或宿主机写入，也不更改 schema 40。
- 文档支持完整 JSON、正文 TXT/Markdown 和纯文本 DOCX；DOCX 复用既有 `buildDOCX`，正文含非法 XML 控制字符时不提供该格式。表格按明确 columns/rows 输出 CSV；拒绝重复列、错宽行、未声明字段、嵌套单元格等会导致静默丢数据的转换，保留完整 JSON 下载。CSV 公式按文本处理并显示提示；不是 XLSX 导出或富文本排版承诺。
- 本批发现并修复已有普通产物创建时的大整数精度丢失：标题补充改为 `json.RawMessage` 对象处理，不再把正文中的数字转成 float64。新存储与下载的 9007199254740993 保持精确；不能恢复已经失真的历史数据，也不代表其他编辑/模型链路的大整数精度已全部审查。
- 主 Agent 和后台 Agent 工具集增加只读 `get_artifact_downloads`，返回后端验证的版本、状态、格式、警告与地址；不声称已保存到用户电脑。增加文档/表格结构和下载回执使用约束。状态化 Worker 的统一工具接线仍属 GAP-09，未被本批替代。
- 前端普通产物工具栏增加下载入口与格式选择；使用当前显示的确切版本，包括历史版本。提案、未保存修改、保存中或版本加载失败时禁止下载；校验回执版本，内容请求失败显示错误且不产生文件。下载使用固定的同源鉴权 API，不跟随结果中的任意 URL；关闭/切换版本后中止请求、忽略迟到结果。
- 本批仅完成普通产物下载接线及隔离验证。Hosted/MCP 输出文件、引用展示、再次作为材料使用、宿主目录写入授权和三模式真实模型任务仍未完成，GAP-02/03/14 保持未勾选，不宣布整个文件交付闭环完成。

G2.3 验证记录（隔离本地，不代表阶段 2 全量真实验收）：

- Go Runtime / HTTP API / agenttool 全包通过：276.874 秒 / 61.625 秒 / 0.682 秒。新增验证覆盖历史版本与数据库重开、文档内容与 DOCX 解包 XML、数字精度、CSV 列顺序/公式/异常结构、跨租户/跨 Agent 项目/只读用户和删除后的拒绝；下载头明确 attachment、nosniff、private no-store。
- Python 相关最终回归 129 passed in 6.29s，临时目录 `.tmp/goal-g2-artifact-delivery-final`。包括原生 SDK SequenceModel 调用下载工具和审计、身份/版本不符拒绝、本地 HTTP 客户端编码与身份传递，以及主执行器、后台、作者工具与审批恢复。新增测试初次缺少目录 fixture 必填字段，补齐后通过，未放宽生产目录校验。
- 前端全量 31 文件、196 项测试通过，TypeScript 构建检查通过。包含版本切换/迟到响应、未保存修改与提案禁用、权限失败、重试、重复点击、下载失败不生成文件和回执任意 URL 不被跟随。
- Playwright 本地 mock API 验证 1440x900 与 390x844：下载内容和文件名、历史版本、错误不下载、未保存修改禁用、Escape/焦点恢复、长标题无弹窗溢出。截图已查看，报告及图片在 `.tmp/goal-g2-artifact-delivery-ui/`。手机端按现有导航切换“工作区”后操作；不是模型、真实作品或真实后端 E2E。
- 隔离浏览器首次启动受 Windows spawn EPERM 限制，经权限审查运行；脚本自行启动临时 Vite 与 fixture，禁用项目默认后端代理，finally 关闭全部测试服务/浏览器，最终退出码 0。未访问或重启 8860、未修改用户作品、网关、模型、凭据，未 push 或部署。

### G2.4 Hosted 输出、引用与作品材料复用

- 工具执行记录按后端资产的 project/source_type/Agent 调用 ID/SDK 调用 ID 显示 Hosted 输出，不信任模型结果里的任意文件 ID。产出可下载或加入当前输入框；作品材料选择器支持搜索、显式选择、去重和同一快照引用，删除/过期/解析未完成/Skill 不支持的材料禁止使用。
- 新增鉴权资产 download 路由，复用资产保留期和租户检查，并补 Agent activity 的项目限制。流式 attachment、Range、nosniff、private no-store，不把大文件全部读入浏览器内存。CSV/JSON 作为文本原文件保存和解析，其他未知输出仍 ZIP 包装，不宣称任意二进制都能再次输入。
- 修复上传契约：DOCX/PDF 使用后端 document 类型，按扩展名规范 MIME，浏览器返回空 MIME 时仍能上传 CSV/JSON/文档等既有支持格式。Hosted 产出回执必须含匹配的 asset_snapshot，缺失或错配记为失败，不能声称已交付。
- Hosted 引用存入既有调用结果的版本化有界 envelope，不改 schema 40；数据库重开仍可显示 URL/提供方文件引用。过滤危险 scheme、凭据 URL、控制字符并限制数量/大小，原始被拒 URL 不留在诊断摘要。提供方 file_id 不是项目下载地址，历史已截断引用不能恢复。
- Go Runtime / HTTP API / agenttool 全包通过：365.844 秒 / 65.059 秒 / 0.666 秒；覆盖真实本地 HTTP 上传、原字节下载、相同快照读取、Range、租户与 Agent 项目拒绝、引用幂等与数据库重开。
- Python 最终相关回归 102 passed in 6.52s，临时目录 `.tmp/goal-g2-outputs-final-reviewed`。首次 stdio fixture 命名管道受 WinError 5 限制，审查后重跑通过；无真实模型请求。前端全量 32 文件 / 215 项通过，TypeScript 检查通过。
- Playwright 隔离 fixture 在 1440x900 / 390x844 验证执行记录文件/引用、下载原字节、材料选择与失效、一次添加和去重、发送精确 attachment_refs，截图已查看无溢出。报告 `.tmp/goal-g2-output-delivery-ui/report.json` 明确 modelCalls=0、userBackendAccess=false；发送由 fixture 捕获后故意拒绝，不是业务提交或真实模型 E2E。全部测试服务与浏览器已关闭。
- GAP-14 仍未全过：MCP 图片/文件产出持久化、提供方文件引用映射、未知二进制复用与三模式真实验收未完成。GAP-12 的 Hosted 结构化输入仍待接通；上述复用证明材料能进入平台请求，不证明代码解释器/图片编辑已拿到字节。未操作 8860、用户作品或测试机。

### G2.5 Hosted 结构化材料输入

- 复核固定 `openai-agents==0.21.1` 的 `Agent.as_tool(input_builder=...)`、`openai==3.3.1` 的 `ResponseInputFileParam`，代码/图片子 Agent 直接复用 SDK 原生结构化输入和 Runner，不增加模型循环或另建文件服务。代码解释器收 `input_file` 原字节，图片工具收 `input_image`；不只把文件名加入 instruction。
- 工具参数增加 `input_assets`，必须是项目 asset_id + asset_snapshot_id，不接任意 URL、宿主路径或提供方文件 ID。无材料的旧请求继续可用，web/file search 保持纯 instruction。当前单次最多 4 个引用，单文件 10 MiB、总计 20 MiB；重复引用去重，同资产冲突版本拒绝。
- BackendClient 复查资产/作品/当前快照、可用状态、长度及 SHA-256，通过携带执行身份的内容 API 读取并比对原字节。内容端点新增可选快照约束和 private no-store，陈旧快照返回 409，不隐式换用新版本。超限流、内容变化、过期/删除/跨项目在模型请求前失败。
- 支持现有上传类型中的文本/CSV/JSON、PDF/DOCX、PNG/JPEG 和 ZIP 进入代码工具；PNG/JPEG/WebP 进入图片编辑/参考生成，实际 kind/MIME/文件名及大小受校验。不是所有 SDK 支持的任意二进制格式均已接入上传/工作区。ZIP 工具入参可用不代表前端材料选择器已允许 archive，后者仍保持既有禁用状态，列入后续二进制闭环。
- SDK 审批仍包住整个输入准备和托管请求。审批前不读取文件内容或请求模型；批准后按原参数恢复，拒绝或取消不发送材料。审批 reason 明示材料文件内容将发送到当前配置的托管服务，参数摘要包含快照引用；未更换模型、网关或凭据。使用内联内容，不新建提供方 Files 上传/清理生命周期。
- Python 最终相关回归 151 passed in 14.55s，临时目录 `.tmp/goal-g2-hosted-inputs-final`；覆盖主对话/后台/兼容层/App 原有回归、原生 SDK 批准/拒绝与序列化后重建、读取失败/取消、真实本地 HTTP 鉴权与快照/校验和/大小拒绝。另以固定 SDK 使用的 `httpx2.MockTransport` 捕获 Responses 原生 wire JSON，文件/图片字节及 native tool 类型保持一致；无真实网关或模型请求。
- Go HTTP API 全包通过 47.929 秒，包含精确内容/陈旧快照/跨租户；随后 Runtime `TestHosted|TestAgentTool` 通过 8.818 秒，包含审批说明。`git diff --check` 通过。审批拒绝的初版 fixture 重复调用同一工具导致 MaxTurnsExceeded，修正为拒绝后输出未执行说明；传输测试最初假设旧 httpx，核对固定 SDK 后使用已有 httpx2，无依赖安装或升级。
- 本批复用 G2.4 的前端材料入口，未重启前端或改 8860。GAP-12 保持未全过：真实网关对内联文件的支持、真实图片编辑/代码结果、状态化 Worker 的统一工具与三模式验收仍待完成；网关不兼容时应报告，不切换提供方或伪称读到了材料。

实现依据：[Code Interpreter 文件输入说明](https://developers.openai.com/api/docs/guides/tools-code-interpreter)、[Image generation 输入图与编辑说明](https://developers.openai.com/api/docs/guides/tools-image-generation)，以及本项目固定 SDK 源码。官方能力不代表当前网关已通过验收。

### G3.1 状态化工具执行身份与防重复执行基础

- 源码 schema 41 增加 `execution_tool_calls`，直接绑定真实 `execution_attempts`，不将状态化任务伪装成后台 `agent_task_attempts`。工具 Begin 参数与 HTTP activity 增加 execution_attempt_id，互斥主对话/后台身份；Python ContextVar、AgentContext 与 Provider 传递同一尝试 ID 和令牌。
- 执行身份解析使用 run 的发起者，而不是项目所有者；检查当前成员权限、项目、会话、任务/步骤/run 状态、当前尝试和有效租约。内部工具服务调用不能省略执行身份。同一 SDK 调用 ID 不允许跨模式或跨尝试复用；写文件等既有副作用门禁共用新的绑定校验。
- Run 取消、尝试过期/失败会取消绑定的未完成工具及待审批记录。凡已开始写入或敏感工具的尝试，不允许因 provider 失败或租约过期自动从头重跑；已存文件/结果保留，过期路径记 `SDK_TOOL_REPLAY_RISK`。这属于保守的重放保护，不是声称能证明外部副作用只发生一次，也不代替 SDK checkpoint 恢复。
- v40 到 v41 仅在临时数据库迁移验证，已有执行尝试和版本文件保留，生成迁移备份记录。测试涵盖令牌/身份/权限撤销、跨尝试幂等、数据库重开、真实文件写入后取消、过期审批取消、provider 失败及失租后禁止自动重复写入。
- G3.1 全包回归通过：Go Runtime 385.381 秒、HTTP API 80.884 秒、agenttool 1.577 秒；Python 相关 204 passed in 27.36s。首次专项测试的 CancelRun fixture 缺少显式确认、HTTP fixture 缺少工具 registry，均修正 fixture，未放宽生产校验。
- 尚未向 SDKTaskWorker 开放统一工具。本批不包含状态化 SDK RunState 审批持久化/恢复、冻结目录接线或三模式真实 E2E，因此 GAP-09 不勾选。

### G3.2 状态化 Worker 续租与取消清理

- 新增内部尝试 heartbeat，绑定尝试令牌和冻结输入哈希，续租时复查 run 发起者的有效成员/编辑权限、项目存续、当前任务/步骤/尝试和租约。过期尝试不可复活，短租期请求不可缩短已有租约；数据库重开保留续租结果。源码仍为 schema 41，不操作运行中的数据库。
- Worker 生成前先确认执行权，在生成、分批执行和输出修复/结果提交期间持续续租。续租失败、超时或回执身份不符时取消执行和监控任务，不继续提交新结果，也不把失租伪报成模型失败来触发自动重试。关停清理两个 asyncio 任务，保留现有每次模型调用的超时规则，没有给大批次增加新的隐式总时限。
- 对正常“暂停到安全边界”继续续租当前任务；真正取消、权限撤销和工作区删除则拒绝续租。heartbeat 与结果提交竞争时，已持久化的 `result_received` 只返回原回执，不续租；Worker 若先收到该回执则走既有 commit，不重新请求模型。
- Sidecar 全量隔离回归通过，393 passed in 36.53s，临时目录 `.tmp/goal-g3-stateful-all-python`。新增 12 项 Worker 测试覆盖慢生成、修复阶段续租、首次/运行中失租、挂起 heartbeat 的超时、关停、回执错配、已存结果和原生 SDK 流取消；真实本地 HTTP fixture 验证内部服务凭据、尝试令牌与输入哈希传输。没有真实网关调用。
- Go Runtime 全包通过，349.320 秒；agenttool 全包通过，0.689 秒。HTTP 首次全包业务断言通过，但非小说重生成测试清理临时数据库时出现 Windows 文件占用，退出码 1；单独重跑通过，48.333 秒，保留首次失败记录。
- 排查发现测试只取消 Agent 管理器而不等待执行协程退出，且出错测试用 defer 关闭 Store 早于 t.Cleanup 取消管理器。新增管理器完成信号及执行协程等待、服务退出时最多 5 秒等待、测试清理的等待与关闭顺序；这修复了明确的资源生命周期缺口，不声称已证明所有 Windows 文件占用都出自同一原因。修改后 HTTP 全包 52.744 秒、cmd/server 1.938 秒通过。
- 新增管理器关闭/等待测试重复 5 轮通过，7.009 秒：确认执行协程结束、Store 仍可读，然后关闭并实际删除临时数据库；同时验证等待有界、未启动时无需等待。所有本批测试命令均已退出，`git diff --check` 通过。
- 首次 Go 专项测试误用了 workspaces 不允许的 disabled 状态，改为已有 deleting 状态，未修改生产约束。原生流取消 fixture 最初在模型启动前就触发失租，改为观测模型已启动后再模拟失租，以便确实验证原生执行器的取消。
- 本批不改前端，不启动/重启服务；不存在已升级 8860 的新验收结论。续租不替代审批等待的持久恢复：下一步仍须先完成状态化 RunState/冻结上下文再开放工具。

### G3.3 状态化审批持久恢复基础

- 源码 schema 42 新增 `execution_run_states`，保存原生 SDK RunState、状态哈希、审批调用集合及独立的有界 Worker 游标。Go 不重建 SDK 消息/调用状态；checkpoint 校验绑定原尝试、令牌、当前权限、冻结输入和全部待审批调用，无运行中工具时才能暂停。
- 审批等待保留同一个 Task/Attempt，不消耗重试次数；全部批准/拒绝后再次领取时重查 run、成员、提供方/模型和精确 Skill 版本，复用冻结 ContextPack，轮换尝试令牌。数据库重开可继续，旧令牌不可执行；损坏/缺失 checkpoint 或变动的 ContextPack 明确失败，不从头生成。
- 支持审批先于 checkpoint 返回的竞争、多调用部分批准、重复 checkpoint 幂等、并发 Worker 仅一个领取。用户暂停时不自动恢复，取消/删除作品时取消尝试和绑定的待审批工具。恢复后失租以及 run 已处于 pausing 时失租，都不能绕过 checkpoint 从头重放写入。
- Python BackendClient 增加内部审批 checkpoint 上传；共享 SDK 上下文序列化加入 execution_attempt_id，但仍不序列化令牌。主对话/后台恢复拒绝其他执行模式身份。原生 SDK Runner/RunState 的隔离模型 fixture 验证批准一次执行、拒绝零执行、重建 Agent 并换令牌后继续原调用，不是 Go 合成 JSON 测试替代 SDK 行为。
- Runtime 首次全包通过，378.270 秒；agenttool 1.390 秒、cmd/server 3.451 秒通过。覆盖 v41 -> v42 临时迁移、备份、已有工具/尝试保留和 8 Worker 并发领取。HTTP 首次全包因管理器关闭后的临时数据库文件占用失败，83.928 秒，不能记作全绿。
- Python 全量隔离回归通过，401 passed in 30.18s，目录 `.tmp/goal-g3-checkpoint-all-python-final`。首次沙箱运行受到临时目录和命名管道权限限制；受审查运行后发现两个后台 fixture 沿用了主对话 ID，已修正 fixture，并增加跨模式拒绝测试，未放宽生产校验。没有真实网关调用。
- 关闭问题独立复现：Store 原先直接返回 `sql.DB.Close()`，连接尚未释放时即可返回；新增有界连接清理和超时错误，持有连接/释放/重复关闭测试通过。但修复后管理器重复测试仍出现文件占用，进一步定位旧 SQLite 驱动取消查询时丢弃未关闭 rows 的路径。新增独立 1000 次取消查询测试，在旧依赖下复现残留 `interrupted (9)` 及数据库文件占用，排除仅为 HTTP 管理器清理顺序的解释。
- 临时 modfile 对照后，采用 `modernc.org/sqlite v1.42.2` 及其匹配 `libc v1.66.10`，不手写数据库驱动或关闭取消能力。[上游取消清理实现](https://gitlab.com/cznic/sqlite/-/blob/v1.42.2/stmt.go) 与 [取消竞争修复记录](https://gitlab.com/cznic/sqlite/-/blob/v1.42.2/CHANGELOG.md) 支持本次依赖选择。Go 最低编译要求由 1.22 提升至 1.24.0，本机已有 1.26.5；没有安装系统 Go 或修改测试机。
- 修复版本下，Store 关闭/取消查询/管理器关闭测试重复 20 轮通过，Runtime 14.128 秒、HTTP 30.565 秒。正式依赖下完整 `go test ./... -count=1 -timeout=10m` 退出码 0，Runtime 459.297 秒、HTTP 104.255 秒，其余所有包通过或无测试。`go mod verify` 全部通过，Linux amd64 / CGO=0 服务端交叉编译通过，产物仅在 `.tmp/goal-g3-checkpoint-server-linux`，没有运行或发布该二进制。
- 前端发现并修复现有事件监听顺序错误：`agent_task.waiting_approval/resumed` 在注册监听之后才加入数组，实际未监听。改为先加入再注册，并接上 `task.waiting_approval/resumed`；状态化任务等待审批显示“等待工具授权”，不再落到“处理中”。新增 5 项测试先复现失败，修复后前端全量 33 文件 / 220 项通过，TypeScript 检查与隔离目录 Vite 构建通过。事件测试挂载实际 App、派发 EventSource 事件，验证刷新及卸载清理；不是仅检索源码字符串。
- 本批未接通 SDKTaskWorker 的统一工具集、Worker 阶段/子批次恢复和冻结目录，因此 GAP-09 仍未通过。前端仅修复上述状态刷新/标签，没有启动服务、迁移用户数据库、push 或部署；schema 42 仅是源码状态。构建输出位于 `.tmp/goal-g3-checkpoint-frontend-dist`，未覆盖现有前端 dist；所有本批命令已退出，`git diff --check` 通过。

### G3.4 状态化冻结 Skill 目录与审批续租竞争

- 源码 schema 43 增加 `run_skills` 和 `runs.skill_catalog_ready`。两条 Run 创建路径均在同一事务冻结目录，复用已有不可变执行包、版本校验和目录加载，不重建 SDK Skill 解析器。相同发起者的 Run 继承创建轮目录及该轮已批准安装的精确版本；无来源轮的 Run 在启动时冻结当前目录。主 Skill 始终采用已确认 invocation 的精确包，不被后来的高优先级同名安装替换。
- 协作者确认启动的 Run 使用该协作者自己的可见目录，不继承另一作者的个人 Skill；读取冻结目录时检查项目、工作区、Run 发起者和互斥执行身份。独立测试通过正常提案/确认/启动链路验证个人目录不会泄漏，未放宽已有角色或确认校验。
- 状态化资源读取接入真实 execution_attempt 的主 Skill 版本与完整 Run 目录。升级、同版本目录改写、源目录删除和数据库重开不改变绑定；禁用/卸载仍撤销可用性，不回落到同名其他包。原先为空的目录不会自动发现随后新装的 Skill。
- 已批准的作者安装回执按当前 execution_attempt 叠加，主对话、后台和状态化三种执行关联互斥；其他 Run/turn 的冻结目录不变。隔离测试验证状态化安装前拒绝、批准安装、资源读取、数据库重开后同回执重试和禁用后拒绝。这里是后端工具链验证，不是实际 SDKTaskWorker 已完成作者任务。
- v42 -> v43 只在临时数据库迁移并验证备份，保留已有 Run/Attempt。旧 Run 缺少完整目录时保持 `skill_catalog_ready=0`，动态 Skill 工具返回 `SKILL_CATALOG_NOT_FROZEN`，不在迁移或审批恢复时偷偷生成新的目录快照；现有冻结主 Skill/业务产物不被改写。运行中的用户数据库未迁移。
- 审批 checkpoint 已持久化但响应未返回时，heartbeat 返回原 waiting_approval 回执，不续租、不返回私有 SDK/Worker 状态；原有权限、取消、令牌与输入哈希检查仍执行。Worker 收到初始等待回执时不重新生成、不提交结果、不报 provider 失败；慢 checkpoint 响应期间仍可接受等待回执。批准后再次领取轮换令牌，旧 heartbeat 令牌失效。损坏或缺失 checkpoint 不被当作成功等待。
- Go 专项最终回归通过 36.243 秒，覆盖冻结目录、来源轮继承/升级、协作者、安装回执、迁移和 heartbeat。Go 全包首轮 Runtime 448.518 秒、HTTP 85.258 秒及其余可启动包通过，但 4 个包的测试程序启动出现 Windows Access denied，因此该命令退出码 1，不能记作全绿。
- 经权限审查重跑，artifactcontract 2.282 秒、executor 3.024 秒、补充 HTTP 审批续租与旧令牌检查后的 HTTP 全包 80.010 秒通过；agenttool/capability 第二次仍被拒绝启动。最后单独串行重跑两包通过：0.828 秒 / 2.023 秒，退出码 0。没有修改 ACL、安全设置或换用跳过测试的方式。所有 Go 包最终均有本批通过记录，但不是一条全包命令一次通过。
- Python Worker 专项 86 passed in 13.58s；Sidecar 全量 403 passed in 38.57s，临时目录 `.tmp/goal-g3-catalog-python-all`。包含初始等待回执零模型重放及慢 checkpoint 响应期间心跳竞争；全量使用本地模型/HTTP/MCP fixture，没有真实网关请求。本批未改前端，未启动/重启服务、push、部署或操作测试机。所有测试命令均已退出。
- GAP-09 仍未通过：实际 SDKTaskWorker 尚未准备统一 AgentToolProvider/MCP，未传 AgentContext/原生 RunState，子批次和输出兼容模式恢复仍待接通。目录与等待回执基础完成，不得将其当作三模式能力一致性或真实 E2E 已完成。

### G3.5 实际状态化 Worker 接入统一工具与原生恢复

- SDKTaskWorker 与 BackgroundTaskWorker 共用 19 个执行工具及 AgentToolProvider，包括项目/资产/产物/对话读取、Skill 资源和声明脚本、工作文件、Skill 草稿校验/安装/加载。MCP 生命周期和 Hosted 工具准备复用既有实现，传入真实 execution_attempt AgentContext；主对话额外的控制/业务提交工具不直接下放给 Worker。视频独立执行路径及业务 Prompt 保持原边界。
- 状态化生成使用原生 RunState 保存与恢复审批，Worker 附加状态仅记录身份、完成子批次、用量、产物读取游标及结构化/JSON 兼容模式，不手工重建模型消息。恢复核对冻结目录、精确版本、SDK Context、输入及审批决策；错误身份或 checkpoint 停止执行，不回到初始生成。Worker 状态大小限制 4 MiB，SDK 状态保留既有 64 MiB 限制。
- 已完成的 40 单元源分析子批次不会在审批恢复时重跑；81 单元测试验证顺序与覆盖，并核对累计 4 次模型请求 / 48 fixture tokens。原生结构化输出兼容改用 Agent.clone 保留工具；调用过工具或从 checkpoint 恢复后不再回退为一次新的 SDK 运行。
- 新测试运行真实固定 SDK Runner/流式结果/RunState 和本地 stdio MCP，分别覆盖批准、拒绝、重建 Worker、丢失 checkpoint 回执、令牌快速轮换、两次审批、已安装 Skill 再次加载、声明脚本恢复、损坏状态和超大 checkpoint。MCP 写入有实际本地文件证据；脚本执行回执由后端 fixture 提供，不是真实 OCI。用量 fixture 按固定 OpenAI ResponseUsage 字段补全，明确断言跨审批的累计用量。
- Sidecar 全量 `427 passed in 65.39s`，报告 `.tmp/goal-g3-unified-python-proof.xml`。这是原生 SDK + 模型/Backend fixture 的集成测试，不是 Go 与 Sidecar 联跑或真实网关验收；后续跨层检查确实发现恢复响应字段和失败码契约遗漏，见下一批记录，不能把 427 项通过当作后端端到端已完成。
- 本批没有前端修改、用户环境迁移、服务启动/重启、push 或部署；源码 schema 仍为 43。GAP-09 保持未完成，真实三模式 UI/后端联合验收、依赖热加载和恢复边缘仍在范围内。

### G3.6 跨层恢复契约、重试安全与恢复队列

- 对照实际 Go JSON 与 Python Worker 后发现 G3.5 单侧 fixture 未覆盖的契约错误：Go 的恢复响应没有 `schema_version`，Worker 会拒绝所有真实恢复；四类新失败码未在 Go 失败上报白名单内，失败请求会被拒绝并留下活动尝试。新增测试先复现，再补真实恢复响应版本及五类失败码（另包括此前遗漏的内容安全拒绝码）。不将失败码宽泛映射成可重试的 Provider 临时故障。
- 原生恢复版本来自已校验的持久 SDK envelope，不采用默认值。Go Store 与实际 HTTP 恢复测试核对序列化字段；Python 继续拒绝缺失/错误版本，并增加初始 claim 的输入哈希与 ContextPack 哈希一致性检查。既有 fixture 中不一致的两个哈希已纠正。
- 手动重试与 available_actions 共用持久风险检查：失败任务的任意历史尝试只要发出写入/敏感调用或保留 SDK checkpoint，就拒绝从头生成。检查不依赖当前 attempt 指针或可被业务配置放宽的 retryable_errors；成功的相邻任务不参与失败项检查。普通无工具/只读失败、尚未开始的批准/拒绝写入保持原有安全重试能力。这里没有新增“确认风险后重放”入口，含副作用任务的结果核对与安全再执行流程仍需后续完成。
- 新增 10 类重试用例，覆盖写入等待/批准/拒绝/运行/完成/失败、批准/拒绝 checkpoint、只读与无工具；均经数据库关闭重开后检查 API 与前端可用操作一致。测试先复现 5 个危险场景能被从头重跑，再验证阻断。失败上报的五类确定性错误持久保存、幂等回执、阶段和不可重试性有专项测试。
- 恢复队列改为事务内有界键集分页，提供方/执行器在 SQL 中过滤，模型/权限/可用性检查失败后继续下一页，不反复卡在前 100 条。三种 101 条不匹配任务的用例先复现饥饿，修复后能领取第 102 条匹配任务，不改写其他任务；同页前一损坏任务使 Run 失败时，重新核对后续任务状态，禁止继续恢复同 Run 的调用。首次普通任务领取的 LIMIT 100 问题尚未修复，不应将本项记作整个调度队列已完成。
- 专项结果：状态化/HTTP 首轮修复后 Runtime 61.900 秒、HTTP 5.073 秒通过；补齐五类错误及恢复队列后 Runtime 专项 27.934 秒通过。Sidecar 全量 `429 passed in 45.08s`，报告 `.tmp/goal-g3-contract-python-final.xml`，回读报告确认为 0 失败、0 错误、0 跳过。
- 最终 Go 全包串行回归 `go test -p 1 ./... -count=1 -timeout=10m` 退出码 0；Runtime 412.747 秒、HTTP 54.579 秒，其余所有包通过或无测试。完整输出 `.tmp/goal-g3-contract-go-all.log`。本轮无需跳过包或修改系统权限，所有测试命令已结束，`git diff --check` 通过。此轮未改前端，未重复运行前端回归；三模式 UI 和真实 Go + SDK 联跑仍不能用这些单侧/HTTP 测试替代。
- 本批仅修改源码与隔离测试，schema 保持 43，没有修改前端、调用真实网关、启动/重启服务、迁移用户数据库、push 或部署。实现中的针对性自查不是用户要求的最终独立全局 review。

### G3.7 Go/SDK 跨进程作者链路与执行归属 UI

- 新增 `sdk_integration` 构建标签下的 Go HTTP + Python SDKTaskWorker 联跑测试。使用临时数据库、随机回环端口和固定 SDK 的真实 Runner/RunState；模型采用确定性序列，不请求外部网关。Worker 经真实 HTTP 编写项目 `skills/http-authored/SKILL.md`、校验草稿、等待安装授权；随后 Python 进程退出，Go Store/HTTP 服务关闭重开，再通过公开审批 API 批准或拒绝，由新 Python 进程恢复。
- 联跑先发现实际产物提交 422：无 response adapter 的动态 Skill 要求直接提交 schema 声明的对象，Worker 却附加 `artifact` 包装。Go ContextPack 现在为这类直接提交下发精确 provider_result_contract；Python 校验该合同的完整顶层对象，不把名为 artifact/provider_result 的合法字段误当旧包装。旧内置适配器包装、checkpoint 和显式 provider schema 保持既有协议；未修改业务 Prompt 或降低后端 schema 校验。
- 批准分支真实安装项目 Skill，并在同一次恢复中加载其指令；拒绝分支不安装，继续完成文档。两分支均提交正式待确认产物；公开 workspace-projection 显示正确工具/产物及归属。断言 attempt ID 与次数不变、文件仍为版本 1、工具数量不重复、累计用量精确为 5/4 次模型请求。完整命令 `go test -p 1 -tags sdk_integration ./internal/httpapi -run '^TestSDKStatefulSkillAuthorHTTP$' -count=1 -v -timeout=5m` 通过，包耗时 50.244 秒，日志 `.tmp/goal-g3-origin-http-sdk.log`。这不是用户自然语言真实模型验收，也未包含新增外部依赖或真实 OCI。
- AgentToolCall 新增公开 execution 归属，从实际对话轮/后台 attempt/工作流 attempt 绑定查询模式、Run、步骤、任务和尝试次数；校验项目、会话和工作区一致，不投影执行 token、SDK 私有状态或 checkpoint。三模式查询、未绑定历史记录与跨范围错绑专项通过；不需要新数据库表，schema 仍为 43。
- 前端通用授权卡显示执行归属、步骤和任务，可展开精确标识；原有批准/拒绝与审批版本/hash 契约不变。执行历史按对话或实际 attempt 分组，区分工作流、后台任务及不同重试；没有来源的数据明确标为未关联。等待授权的后台任务不再因早于最近 12 条消息而隐藏，仍可取消。
- 前端定向 27 项、全量 224 项 / 33 文件通过，TypeScript 与隔离输出目录 Vite 构建通过。新增 `execution-origin-ui-smoke.mjs` 自建临时 Vite/模拟 API，桌面 1440x900、手机 390x844 均验证归属分组、长标识换行、无横向溢出/按钮重叠、原审批批准/拒绝请求和较早待授权任务可见。四张截图与报告在 `.tmp/goal-g3-origin-ui/`，已逐类目视检查；浏览器 API 为 fixture，不能冒充真实模型三模式端到端。
- Python 定向首次有 98 项通过、3 项被 Windows 临时目录权限阻止；在隔离范围经权限审查重跑为 101 项通过。最终 Sidecar 全量 `432 passed in 62.45s`，报告 `.tmp/goal-g3-origin-python-all.xml` 回读为 0 失败、0 错误、0 跳过。浏览器启动也经权限审查重跑，未修改 ACL 或系统配置。
- Go 全包回归 `go test -p 1 ./... -count=1 -timeout=10m` 退出码 0，Runtime 510.344 秒、HTTP 52.012 秒，其余全部包通过或无测试；日志 `.tmp/goal-g3-origin-go-all.log`。`git diff --check` 通过，所有测试命令已退出。本批针对性自查不等同最终独立全局 review。没有 push、部署、操作测试机、重启/切换 8860/8880、修改模型/网关/凭据或迁移用户数据库。仅启动隔离测试所需临时进程，跨进程与浏览器测试均已关闭其端口。

### G3.8 当前执行内激活新 Skill 的脚本和 MCP 依赖

- `PreparedAgentTools` 复用固定 SDK 的 `MCPServerManager` 管理连接，以原生并行连接生命周期任务保证 connect/cleanup 处于同一 asyncio task。动态安装只更新 SDK Agent 共用的服务器列表；没有增加模型循环、每 Skill 专用执行器或自建 MCP 协议栈。
- 新安装/升级的精确 inline Skill 通过可用状态、版本、内容哈希与 scope 校验后，可在当前执行激活其脚本及已配置 MCP 依赖。脚本使用原生 FunctionTool.is_enabled 动态可见性；已有 SDK guardrails、审批、版本快照和后端审计继续生效，未选择脚本时不向模型暴露执行工具。
- MCP 激活仅使用本次执行起始时已加载的受信任工具配置，复查启用与 allowlist，并连接、实际列举所声明的工具。扩大同一服务器的允许工具集合时建立新连接，旧连接保留至执行退出，以免取消已开始的并行调用。相同定义重用连接，并发安装通过执行内锁保留各自的依赖集合。
- 缺失、禁用、未列入 allowlist、连接失败、缺脚本工具、只读范围不允许的依赖均不发布新执行权限。已成功安装的回执保留 installed_as_skill=true，但 ready_in_current_turn=false；取消激活会清理新连接，不把未就绪状态写进已加载目录。连接对象不进入 SDK checkpoint。
- 当前执行保存新增 Skill 的完整已验证元数据，后台恢复不再一律排除带依赖的 inline Skill，而是核对 checkpoint、冻结目录与版本后重新准备工具。后台/状态化作者不能通过安装替换其自身主执行 Skill 的固定版本。
- 实际主对话 `OpenAIAgentsRuntime` 流式入口、原生持久 Session 和 RunState，在重建执行器后验证安装审批、加载新 Skill、新增脚本或带服务名前缀 MCP 工具的第二次审批，再批准/拒绝并唯一提交。批准 MCP 分支确实写入本地 JSONL 一次，拒绝分支不写；脚本后端是记录调用的 fixture，不是真实 OCI。主对话四种组合 `4 passed in 12.13s`，报告 `.tmp/goal-g38-main.xml`。
- 实际后台 Worker 覆盖无依赖/新 MCP/新脚本、两次 Worker 重建和审批；状态化 Worker 覆盖原本未加载的新 MCP 依赖、二次审批及恢复，实际 SDK 协议与本地 stdio 运行，模型和业务 Backend 仍为 fixture。背景测试首次因适配器遗漏 configuration_hash 参数失败，主对话测试首次缺提交参数和标准 inline entry_policy；对照正式接口补齐 fixture，并增加工具失败及实际副作用断言，未放宽生产契约。
- 最终 sidecar 全量 `450 passed in 66.75s`，报告 `.tmp/goal-g38-python-all.xml`；包括连接生命周期、失败、取消、并发、SDK guardrails 和三个执行器既有回归。Go/SDK 真实 HTTP 跨进程作者验收再次通过，`TestSDKStatefulSkillAuthorHTTP` 的批准/拒绝分支包耗时 29.992 秒，仍为无新依赖场景和确定性模型，不得与本批分层测试拼接为带依赖的真实模型 E2E。
- 本批未改变 Go schema（仍 43）或前端，未重复前端/Go 全包回归；共享 sidecar 全量和既有 Go/SDK 跨进程测试均已结束。`git diff --check` 通过。未更改用户运行环境、模型、网关、凭据，未部署、push 或操作测试机。
- 剩余边界：未接 MCP 凭据动态消费或新服务器热注册，未为新装 Skill 动态增加 handoff，也未解除普通未路由 Skill 的加载边界；真实 OCI、真实网关含依赖作者任务及完整三模式前后端同场验收仍未完成。GAP-01/09/13/16 保持未勾选，本批不是最终独立全局 review。

### G4.1 主对话控制 Run / 后台任务与安全重试

- 主对话新增 `list_execution_targets`、`inspect_execution_controls`、`control_execution`，由固定 SDK FunctionTool、统一工具审计、原生审批/RunState 和已有 Session 执行。没有另建 Agent 循环或任务状态机，也没有把不受支持的 `control_run` intent 填入 Python 决策。控制结果仍由已有主对话提交工具提交。
- 目标列表按项目和类型进行 50 条键集分页，包含稳定 ID、能力、状态与创建时间；详细快照与 available_actions 按需读取。工具指令要求有歧义时确认目标，禁止根据列表顺序或 UI 焦点推断。Run 四类操作复用原暂停/继续/取消/失败步骤重试，后台复用取消/安全重试。后台暂无暂停/继续，不把取消后从头重试冒充恢复。
- 写控制始终需要 SDK 审批，卡片标识具体动作与目标。内部写端点要求服务认证、活跃主对话与委托用户；在原命令事务内重读成员权限、审批、工具参数哈希、SDK call ID、执行归属及目标快照。状态变化返回 409，必须重新检查和确认，不能把原批准套到新任务或新状态。后台/工作流执行器没有这些生命周期控制工具，也不能自行冒用主对话调用。
- 原命令使用 `agent-control:<call_id>` 幂等键保存回执。响应丢失、数据库重开和 Run 已推进到无当前步骤时，重试只读取原命令结果，并重新校验当前访问权限，不再次执行。审批不等于执行成功，`pausing` 不等于 `paused`，`queued` 不等于任务完成；模型只收到准确的紧凑结果。
- 修复后台手动重试未检查历史副作用的问题：任意历史 attempt 已开始的写入/敏感调用或保留的 SDK checkpoint，均阻止从头重新执行；不只检查最后一次 attempt。普通只读、尚未开始的写入仍可安全重试。详情、列表、取消即时回执及 SDK 可用动作共用检查，前端隐藏危险的“重新执行”并显示原因。取消待审批任务删除未执行 checkpoint 后可重新执行，但已发出写入的历史风险保留。
- Go 专项覆盖四种 Run 操作、后台取消/重试、权限撤销、拒绝、参数篡改、SDK ID 不匹配、停止的对话、其他项目、绕过控制工具的公共命令、53 条分页和历史重放风险。HTTP 测试使用真实路由、认证和公开审批 API，覆盖批准/拒绝、过期快照、只读用户、跨工作区及缺失服务执行身份；测试 Skill 起初未绑定命名工作区导致不可用，修正 fixture 归属后通过，未放宽生产权限。
- 主对话原生 SDK 测试覆盖六动作乘批准/拒绝，重建 Runtime 后恢复、唯一调用与唯一提交；Backend HTTP 测试核对 URL 编码、项目身份头和工具参数。Sidecar 全量 `467 passed in 92.71s`，`.tmp/goal-g41-python-all.xml` 回读为 0 失败、0 错误、0 跳过。模型与业务 Backend 为 fixture，不等于真实模型/Go/前端同场验收。
- 前端定向 25 项、全量 226 项 / 33 文件通过；TypeScript 和独立 `.tmp/goal-g41-frontend-dist` 构建通过，未覆盖原 dist。扩展执行归属浏览器脚本，1440x900 / 390x844 验证控制审批长标识、原审批批准/拒绝、历史分组、较早等待任务及重试限制提示；截图和报告在 `.tmp/goal-g41-control-ui/`。API 为模拟数据，临时 Vite/HTTP/浏览器均自行关闭，不代表用户 8860 已更新。
- Go 全包命令 Runtime 456.818 秒、HTTP 62.612 秒及其余包通过，唯 agenttool 的旧数量断言为 26 而实际增加到 29，故该命令退出 1，日志 `.tmp/goal-g41-go-all.log`。修正数量并增加三个控制工具的读写、审批及禁用自动重试断言，agenttool 全包单独重跑 2.817 秒通过；取消即时回执补齐后，控制/后台/HTTP/目录专项最终 Runtime 48.874 秒、HTTP 10.192 秒、agenttool 1.198 秒全部通过，退出码 0，输出 `.tmp/goal-g41-go-controls-final.log`。不宣称第一条全包命令一次全绿。所有测试命令已退出，最终 `git diff --check` 通过。
- 本批仍是第一阶段功能补齐中的实现与定向自查，不是最终独立全局 review。源码 schema 保持 43；没有请求真实网关、改变模型/凭据、安装 OCI、迁移用户数据库、push、部署、操作测试机或切换/重启 8860/8880。含副作用失败的安全再执行、后台暂停/继续及真实三模式联合验收继续保持未完成。

### G4.2 后台暂停/继续的原生 SDK 接线与隔离验证

- 对照本项目固定 SDK 源码 `RunResultStreaming.cancel(mode="after_turn")`、`run_loop` 与 `RunState`，新增两项确定性原生 Runner 测试。用户停止请求在模型响应后到达时，普通本轮工具完成并保存其结果，但不开始下一轮；RunState 经临时 JSON 文件持久化、重建 Agent、恢复后不重放已完成写入，用量累计 24 fixture tokens。
- 当同一轮工具需要批准时，暂停不绕过原生审批；保存与恢复后仍有原 pending item，批准后才执行一次。前置两项测试 `2 passed in 9.30s`，报告 `.tmp/goal-g42-native-pause.xml`。这一时点只证明固定 SDK 的适用语义，以下记录随后完成的平台接线，不将前置验证本身当作功能交付。
- 实际 BackgroundTaskWorker 改用固定 SDK `Runner.run_streamed`；从初始进度或续租回执观察用户暂停，调用原生 `cancel(mode="after_turn")` 并排空事件，不增加自建模型循环。普通暂停保存 SDK RunState 和已完成工具输出，审批暂停保存原 interruptions；原输出校验、兼容模式和 guardrails 保留。从恢复状态或工具执行后不得降级为一次新的 SDK 运行。
- Go 复用既有任务、尝试和 checkpoint 表，增加 `pausing`/`paused` 文本状态，无新表/列或 schema 迁移。排队任务直接暂停且不领取；等待审批时暂停保留原审批；运行中请求先进入 pausing，在 SDK 轮边界保存成功后才 paused。不能撤销尚未完成的 pausing 请求来冒充恢复。最终输出已完成时允许完成胜出；暂停中失联/失败不自动从头重试。
- 继续时复查项目写权限、主 Skill 固定版本和范围、checkpoint 哈希、尝试归属及审批集合；若还有待处理授权，回到 waiting_approval 而非跳过。审批在 paused 状态解决不会自动排队，也不能开始已经批准的工具。实际恢复复用原 attempt ID/次数并轮换 token；旧 Worker 不能再提交 checkpoint。取消、删除和历史写入防重放规则仍有效。
- 用户 API 和前端任务卡新增暂停/继续，主对话控制工具增至八种动作，复用同一 Go 命令与 SDK 审批/幂等门禁。前端区分暂停中和已暂停，只有后者可继续；按钮具备忙碌、错误和取消状态。较早的暂停任务不被最近 12 条消息裁掉，三个暂停/继续事件已注册刷新。
- 新增实际 SDK Worker 测试先复现普通暂停无法恢复：原 `_apply_approval_decisions` 强制要求非空审批。仅在后台恢复没有 interruptions 且没有决策时跳过审批应用；有待审批时仍执行原严格校验。缺决策列表、空决策和错 call ID 均停止且不当作可重试 provider 错误。覆盖首次模型请求前暂停、已完成读取、批准写入后再次暂停、拒绝、回执丢失、最终输出竞争和同一事件循环内取消清理。
- Go/SDK 跨进程联跑通过三分支：真实 HTTP 写 `skills/background-pause/SKILL.md` 后暂停，关闭重开 Go Store/服务，以新 Python Worker 恢复；可直接完成，也可校验草稿、申请安装、再次暂停及重建后批准/拒绝。通过公开用户 API 解决授权后仍 paused，明确继续才安装或拒绝后完成。核对文件版本仍为 1、原尝试 ID/次数不变、工具调用不重复、正式产物保存、累计 2/4 次模型请求及正确执行归属。`TestSDKBackgroundPauseHTTPRebuildAndApproval` 包耗时 51.959 秒，日志 `.tmp/goal-g42-sdk-background-http.log`。模型是确定性序列，不是外部模型验收；初版 fixture 导入不存在的旧 httpx，改用项目已安装 httpx2 后通过，未安装依赖。
- Sidecar 最终全量 `482 passed in 109.05s`，`.tmp/goal-g42-python-all.xml`；前端全量 `235 passed` / 33 文件，TypeScript 和独立 `.tmp/goal-g42-frontend-dist` 构建通过。桌面 1440x900 / 手机 390x844 的暂停、继续、刷新后保留、SSE 更新、较早任务、原审批和重试警告检查通过；暂停按钮 36x36、无横向溢出，截图已目视检查，报告 `.tmp/goal-g42-control-ui/report.json`。浏览器 API 为 fixture，不把分层通过合并宣称为真实模型三模式 E2E。
- Go 暂停/控制专项 Runtime 33.814 秒通过，HTTP 暂停/认证/重启/令牌专项 4.687 秒通过；HTTP 初版测试把 204 无任务响应交给 JSON 解码 helper，纠正测试读取方式后通过。最终 `go test -p 1 ./... -count=1 -timeout=10m` 退出码 0，Runtime 527.564 秒、HTTP 64.921 秒，其余包全部通过或无测试，日志 `.tmp/goal-g42-go-all.log`。全包启动后补加的删除边界和已批准工具在 paused 状态不能开始的测试，单独重新运行全部 `TestBackgroundTaskUserPause` 通过，Runtime 43.577 秒，日志 `.tmp/goal-g42-pause-boundaries.log`。所有测试命令均已退出，Python 报告回读为 482 项、0 失败、0 错误、0 跳过。
- 本批仍处于阶段 1，不是最终独立全局 review。schema 保持 43，未改变用户 8860/8880、模型、网关或凭据，未请求外部模型、安装 OCI、迁移用户数据库、push、部署或操作测试机。真正模型联合验收、含副作用失败的安全再执行及其他基线缺口仍未完成；GAP-11 保持未勾选。

### G4.3 下一轮队列编辑与原生追加语义验证

- 继续使用既有 `agent_turns` accepted 队列，没有新增第二套队列或数据库 schema。新增 `PATCH /api/v1/agent-turns/{id}/queued-message`，只替换本人尚未开始的请求文本，保留附件、选区、客户端上下文和冻结 Skill 目录。当前内容比较、状态和 checkpoint 门禁与写入在同一事务内；已开始、等待审批、恢复为 accepted、终态及含 checkpoint 的记录不能通过此接口改写。
- 命令持久幂等；原创建消息的回执重试仍找到同一条已编辑消息，不重新排队或恢复旧文案。编辑回执丢失后，即使该轮已经被领取，重试也返回当前状态、不再修改执行输入。当前编辑者必须有作品写权限且是消息原作者，跨工作区、其他作者、只读用户、被删除作品和 Agent 工具上下文拒绝。接口拒绝未知字段，不能藉文本编辑更换附件或执行身份。
- 前端显示具体排队内容，新增编辑/保存/放弃图标并复用取消入口；已有恢复状态与新消息分别显示“等待恢复执行”和“消息排队中”。同一对话有活动轮时发送按钮明确为“加入下一轮队列”，仍可连续提交下一轮请求。保存用打开编辑器时的原内容，不以迟到快照自动覆盖草稿；跨窗口变更/开始执行时禁用保存，HTTP 失败保留草稿。新增 `agent.turn.queued_updated` 事件刷新，其他作者/只读用户和已开始请求不显示编辑入口。
- 固定 `openai-agents==0.21.1` 的真实 Runner/RunState/SQLiteSession 测试确认：`add_input` 顺序和深拷贝、序列化重建、会话重开、原工具只执行一次、追加内容只入会话一次、guardrail 拒绝后输入仍 pending、终态/耗尽轮次拒绝。另验证审批未解决时不会因追加而被批准，批准/拒绝各自保留原生语义。
- **关键限制已实测**：已批准的工具可能在新输入 guardrail 和下一次模型调用之前执行；`add_input("不要写入")` 本身不撤销旧授权。`stop_on_first_tool`、匹配的 stop-at-tool 和自定义 callable tool_use_behavior 的审批中断也可能拒绝追加。主对话现有自定义终止行为不能绕过此门禁直接接入。生产“当前执行追加”的安全暂停、持久输入身份/去重、审批处理、收件/生效回执及三模式接线仍未完成；本批没有声称队列编辑等于修改当前执行，也没有重建 SDK 消息或实现本地摘要替代原生 Session。
- Go Runtime 队列/Agent Turn/控制相关回归 `54.861s`，日志 `.tmp/goal-g43-go-runtime.log`；HTTP API 全包 `64.848s`，日志 `.tmp/goal-g43-go-http.log`。覆盖临时数据库重开、原创建与编辑重复回执、stale 内容、领取前后、原生恢复状态、权限和删除边界。首轮 fixture 未 bootstrap 新工作区、helper 自动添加幂等键及键复用错误预期状态不符，均修正测试而未放宽生产逻辑。
- SDK 相关回归 `60 passed in 36.63s`，`.tmp/goal-g43-sdk-regression.xml` 回读 0 失败/错误/跳过。包含原生输入、暂停、审批恢复、后台/状态化和 guardrails；模型为确定性 fixture，无真实网关调用。前端全量 `244 passed` / 34 文件，TypeScript 与 `.tmp/goal-g43-frontend-dist` 隔离构建通过。
- Playwright 在 1440x900 / 390x844 验证编辑、保存后刷新、跨窗口 SSE、冲突草稿保留、取消、作者/已开始门禁；按钮 36x36，页面无横向溢出，截图已目视检查。报告 `.tmp/goal-g43-queue-ui/report.json` 明示 API 为 fixture、modelCalls=0、userBackendAccess=false，不是三模式真实模型 E2E。脚本已关闭随机端口服务和浏览器，所有本批测试命令已退出。
- 仍处阶段 1；源码 schema 保持 43，未迁移运行中数据库，未访问/重启 8860/8880，未改模型、凭据或网关，未部署、push 或操作测试机。GAP-15 保持未完成，最终独立全局 review 未开始。

### G4.4 后台当前执行追加要求与模型收件回执

- 源码 schema 44 增加 `agent_task_inputs` 和独立的追加暂停标记。`POST /api/v1/agent-tasks/{id}/inputs` 只接受当前有编辑权限的任务发起者提交文本，不允许其他协作者、只读用户、跨工作区或 Agent 工具身份冒充用户追加。每条最多 32 KiB，每任务最多 128 条 / 512 KiB；独立不可变记录保留原请求、参数和冻结 Skill，不把追加伪装成另一个任务或改写原消息。
- 正在运行的任务收到追加后进入 pausing，经 heartbeat 通知原生流式 Runner 使用 after_turn 保存 RunState；没有待审批 SDK 调用时自动排回原尝试恢复。已有审批中断时保持 paused，必须由用户核对授权并显式继续；追加本身不批准、撤销或改写原工具决策。手动暂停可覆盖追加触发的自动继续，手动暂停期间追加不会恢复任务。
- Worker 复用固定 SDK 的 `RunState.add_input`；首次领取时将原请求和追加文本作为有序原生 user 输入，不改写历史消息或使用本地摘要。保存有界的输入 ID 前缀，恢复检查任务、次序、重复、原 Context 与冻结版本；两次追加、再次暂停及重建 Worker 后不会重复注入。SDK 拒绝追加的 checkpoint 明确返回状态兼容错误；追加输入 guardrail 拒绝不归类为可重试 Provider 故障。
- 回执区分 received 与 included。只有观测到原生模型完成响应、且 SDK 已接纳 pending input，才在 checkpoint/完成事务内确认对应尝试领取过的输入。收到请求不意味着模型已看到，更不证明模型遵守；完成竞争中来不及送入的输入继续显示“未确认送入模型”。模型/工具失败期间未持久确认的输入也不得伪报成功；既有后端失败清理/防重放仍适用，不声称失败后一定可恢复原 checkpoint。
- 前端后台任务卡新增图标入口、文本草稿、提交/收起与持久回执列表，复用事件刷新。发送失败、任务结束或编辑期间权限撤销均保留草稿，禁止无权限提交。连续丢失回执后的手动重试复用同一草稿的幂等键，避免再次点击重复追加；确认成功后再发送或改成另一条内容使用新键。该重试状态保留在当前组件，不声称未提交草稿已跨页面持久化。界面明确接收与送入模型的区别，并说明已批准工具可能先执行，再将追加要求交给模型；没有承诺追加“不要写入”可以撤回旧副作用。
- Go HTTP + 真实固定 SDK Worker 跨进程四分支通过，`146.196s`，日志 `.tmp/goal-g44-input-sdk-all-http.log`：普通暂停继续、批准安装、拒绝安装、运行中追加并自动恢复。每阶段新建 Python 进程并重开数据库/HTTP，核对原尝试、输入只出现一次、实际项目文件版本不重复、安装批准/拒绝和累计模型调用。模型为确定性 fixture，不是外部网关或三模式真实 E2E。
- Sidecar 首次全量 500 项、补状态错误分类后全量 `501 passed in 101.37s`；输入/暂停/后台/guardrails 最终边界 27 项通过，`.tmp/goal-g44-input-boundaries.xml`。增加 guardrail 分类后的最终全量 `502 passed in 127.56s`，`.tmp/goal-g44-python-verified.xml` 回读 0 失败/错误/跳过。一次最终重跑命令的 PowerShell 临时目录参数拼接失败，pytest 在收集前退出 4；已修正参数，不修改测试或绕过临时目录保护。
- 前端最终全量 `252 passed` / 35 文件，`.tmp/goal-g44-frontend-final.xml` 回读 0 失败/错误/跳过；TypeScript 与 `.tmp/goal-g44-frontend-dist` 独立构建通过。7 项追加组件/API 测试包括手动重试幂等键透传与新内容换键。Playwright 1440x900 / 390x844 验证追加、刷新保留、SSE included 回执、暂停/继续、原审批及旧任务历史；无横向溢出、图标控制 36x36，输入及按钮截图已目视检查。报告 `.tmp/goal-g44-input-control-ui/report.json` 是独立 API fixture 验证，最终幂等键接线后再次运行通过，随机端口服务和浏览器均已关闭。
- Go 首次全包 Runtime 在临时 Skill 清理时 Access denied，随后达到每包 12 分钟超时，退出码 1；日志 `.tmp/goal-g44-go-all.log`，不记作通过。受审查隔离重跑失败涉及的两个测试及新发起者/权限测试，重复三轮 `28.377s` 通过，`.tmp/goal-g44-go-boundaries-recheck.log`。最终 `go test -p 1 ./... -count=1 -timeout=20m` 退出码 0，Runtime `650.343s`、HTTP `73.729s`，日志 `.tmp/goal-g44-go-all-recheck.log`。没有修改 ACL 或删除用户数据来掩盖失败；本批所有测试命令均已退出，`git diff --check` 与涉及 Go 文件的 gofmt 检查通过。
- v43 -> v44 仅在临时数据库迁移，核对原 paused 任务、原生 checkpoint 和迁移备份保留。仍处阶段 1，未重启/切换 8860/8880，未迁移用户数据库、调用外部模型、变更网关/凭据、安装 OCI、push、部署或操作测试机。GAP-15 保持未完成，最终独立全局 review 未开始。
- 未完成范围继续保留：主对话和 stateful_workflow 的当前执行追加、附件/二进制追加、已接收输入的撤回/修订、失败后的核对和安全继续，以及完整三模式真实模型联合验收。后台局部通过不代表这些能力已完成。

### G4.5 主对话原生暂停基础与恢复边界

- 固定 SDK 的真实 Runner/SQLiteSession 三分支验证了自定义 `stop_after_commit` 的可用路径：先应用原批准/拒绝决策，以原 RunState 恢复并立即请求 after_turn；旧工具处理完成后不发起新模型请求。若旧工具已经提交终态，则不能再追加；否则新的原生 RunAgain 状态允许 add_input，序列化/重建后追加输入只入模型与 Session 一次。没有修改 SDK 私有字段，也没有把追加解释为撤销旧授权。
- 主对话 Sidecar 流增加独立暂停信号、原生 after_turn 和 `agent.turn.paused` 非终态 checkpoint；取消仍即时终止并优先于暂停，已经提交的业务结果优先于迟到暂停。每条流隔离信号，结束后不再接受暂停。新增受内部 bearer 认证的 `POST /internal/v1/agent/runs/{id}/pause`，同一 run ID 的重复执行在启动前返回 409，避免覆盖原运行句柄。
- 最终提交修复阶段也改用原生流式 Runner。checkpoint 保留主执行/修复阶段、修复次数和候选回复，重建后恢复对应 Agent，不重新执行主 Agent 或已经完成的工具。复现并修正了“第一次修复暂停、恢复后返回空候选、进入第二次修复时丢失原候选”的问题。
- 修复阶段的 JSON-only 兼容层沿用原非流式的控制决策校验，仅对原生 response.completed 做相同的提交工具转换；保留响应身份、用量、事件顺序及其他事件，已有真实工具调用和非法决策不转换。没有放宽 commit 校验或改变网关、模型配置。
- 实测发现固定 SDK 的零模型 checkpoint 直接作为 resumed 输入时会跳过初始输入 guardrail，并可能漏存新输入到 Session。新增严格零工作边界：current_turn=0、无 current_step、模型/工具输出、session_items 或 pending_input；核对保存的初始输入与原生 original_input 后，才让这个尚未执行模型/工具的运行进入第一次原生 Runner。主对话保存精确初始输入，后台按不可变 claim 和已暂存输入前缀核对；任何已经执行的 checkpoint 仍必须恢复原 RunState。没有手写会话记录、重建模型历史、本地摘要或 recent-N 替代。
- 连续两次零模型暂停、历史 Session 保留、原输入仅保存一次、主对话/后台初始 guardrail 拒绝、非法恢复阶段/次数/初始输入，以及审批批准/拒绝/无决策后重建均已补确定性测试。HTTP 测试使用实际 Sidecar ASGI 和 SDK Runner，验证认证、未知轮、重复执行、重复暂停、取消竞争和完成后不可控制；业务 Backend 与模型均为 fixture，不是 Go/真实网关/前端联合 E2E。
- 初始专项 `.tmp/goal-g45-initial-input.xml` 为 28 项通过；第一轮全量 `.tmp/goal-g45-all.xml` 为 `537 passed in 96.07s`。后续新增修复空候选测试先失败并已修复；阶段性全量 `.tmp/goal-g45-final.xml` 为 539 通过、3 个新测试失败，原因是测试错误地要求 Pydantic 构造前后的列表保持对象身份，已改为比较完成事件构造后的原对象和完整内容。最终全量 `542 passed in 127.90s`，退出码 0，`.tmp/goal-g45-verified.xml` 回读 0 失败/错误/跳过；所有本批测试命令均已结束。失败报告保留，不合并宣称每次命令全绿。
- **交付边界：本批是主对话暂停/追加的 Sidecar 基础，不是完整用户功能。** Go manager 尚无暂停通知、持久 pausing/paused 和无审批 checkpoint 接线；Go shell 尚不接受新 paused 事件，用户 API/UI 和主对话输入记录尚未接通，因此不得提前调用新内部暂停路由替代完整链路。状态化追加也未完成，GAP-15 保持未勾选。
- 源码 schema 保持 44；本批没有更改 Go/前端、启动新服务、迁移用户数据库或更新 8860/8880，没有请求真实模型、变更依赖/网关/凭据、安装 OCI、push、部署或操作测试机。仍处阶段 1，最终独立全局 review 未开始。

### G4.6 主对话暂停的 Go 状态、用户入口与恢复调度

- 源码 schema 45 为主对话增加 pausing/paused；用户暂停未开始请求时直接停在队列，运行中则等待原生 SDK after_turn checkpoint。公开 pause/resume API 只允许当前有写权限的原作者操作，并持久保存命令幂等回执；迟到的暂停重试不能把已经继续的轮次再次暂停。继续保留原轮次、请求、开始时间和冻结 Skill，不创建替代执行。
- Go shell 接受非终态 `agent.turn.paused` 并将其视为当前传输的最后事件；manager 向受认证的 Sidecar 暂停路由发送请求，尚未注册运行句柄时重试。已保存 checkpoint、但原传输尚未关闭时，即使用户已经继续，也排除仍有活动句柄的同一 turn ID，避免重复启动。
- checkpoint 沿用私有 RunState 表，新增无审批暂停与哈希/schema/审批账本核对。manager 以 RawMessage 保存原生 JSON，保留大整数精度，不将状态投影到前端事件。暂停中允许已经开始的 SDK 当前轮完成工具和提交；完全暂停后不能启动工具。手动暂停中的批准/拒绝不会自动恢复，显式继续时仍有未决授权则回到等待审批。取消优先，已经提交的终态不被迟到暂停覆盖；缺失或损坏的已开始状态禁止从头重跑。
- 主对话列表保留最近历史之外的所有非终态轮，避免较早的暂停被最近 50 条记录挤掉。补原消息 52 条后续历史、恢复队列、数据库重开、审批交错、取消、提交竞争及版本迁移测试。源码迁移按列名保留旧字段、冻结目录和外键；原 v43 迁移测试更新为核对当前 schema 的实际备份记录，而不是硬编码 to_version=44。
- 删除边界新测试复现：删除作品后等待审批/暂停/暂停中主对话仍保持活动状态。已在删除作品和工作区的同一事务复用主对话终止逻辑，取消旧工具/审批、写终态事件并清理私有 checkpoint，避免不可恢复记录继续占配额。项目三分支先失败后通过，项目/工作区六分支均随最终全包通过。
- 前端主对话卡新增暂停/继续图标，区分暂停中、已暂停、等待恢复和等待授权。权限、忙碌、失败重试幂等、SSE/刷新和原请求展示已接通；暂停图标不旋转，已授权工具不再虚称正在恢复。目视截图另发现原消息长英文串横向被裁切，已补正常换行并将消息气泡纳入尺寸断言。
- 前端专项修正遗漏测试导出后 36 项通过；完整回归先 265 项，补三种主对话 SSE 完整事件/跨项目/卸载测试后最终 `268 passed` / 36 文件，`.tmp/goal-g46-frontend-verified.xml` 回读 0 失败/错误。TypeScript 与独立 `.tmp/goal-g46-frontend-final-dist` 构建通过，不覆盖用户 dist。
- Playwright 1440x900 / 390x844 最终通过暂停中不可提前继续、SSE、刷新保留、较早记录、手动失败重试同一键、其他作者/只读门禁和取消；图标 36x36，消息和控制区无横向溢出，已目视检查截图。报告 `.tmp/goal-g46-main-pause-ui/report.json` 明确是模拟 API、0 模型调用、未访问用户后端。初版 fixture 的空 SSE 被前端严格协议拒绝，之后重复文本定位失败；两处均只修正测试，没有放宽生产协议。所有浏览器验收服务已关闭。
- Go 初次全包未通过：`.tmp/goal-g46-go-all.log` 记录旧迁移备份目标版本断言、两个临时 Skill 文件清理 Access denied，以及未规范化 GOTMPDIR 导致的视频预览路径字符串比较失败。未改 ACL 或视频逻辑；规范化路径后 HTTP 全包 `164.447s` 通过，`.tmp/goal-g46-go-http-recheck.log`。修正迁移断言和删除逻辑后，暂停/审批/删除/迁移定向回归 `30.694s` 通过，`.tmp/goal-g46-go-boundaries.log`。随后 `.tmp/goal-g46-go-all-verified.log` 运行中提前退出 1073807364，未完成 Runtime；同一时段 Worker 联跑也退出该码，原因未确认，不记作通过。核对原 go/runtime/httpapi 测试进程均已结束后才重新启动。
- 最终 `go test -json -p 1 ./... -count=1 -timeout=20m` 退出码 0，结构化日志 `.tmp/goal-g46-go-all-resumed.jsonl` 回读无 fail 事件，13 个有测试包通过，其余无测试；Runtime `651.434s`、HTTP `77.135s`。现有后台及状态化 Go+SDK 跨进程回归 `143.739s` 通过，`.tmp/goal-g46-sdk-workers-resumed.log`，包含后台四分支和状态化批准/拒绝。所有命令已退出，不将此前中断命令合并说成通过。
- Go manager 与实际 shell/本地 HTTP 模拟 Sidecar 联跑验证信号重试、关闭中的旧流排除、原轮次恢复和私有 JSON 精度。进一步新增真正的 Go HTTP + Sidecar ASGI/原生 Runner/SQLiteSession 跨进程测试：模型通过公开用户 API 请求暂停，经 Go manager 的内部暂停路由通知 SDK；SDK 完成本轮项目文件写入后保存 checkpoint。停止并重建 Go 数据库/HTTP 服务和 Python Sidecar，公开 resume 继续同一轮，最终提交回复；断言原开始时间、文件版本 1、工具只执行一次、每进程 1 次模型请求、Session 用户输入及文件工具输出各一份。`TestSDKMainPauseHTTPRebuildKeepsOriginalTurnAndSession` 包耗时 `15.980s` 通过，`.tmp/goal-g46-sdk-main-pause-native.log`。
- 新主对话 fixture 的初版存在 Go 指针比较编译错误、遗漏非空幂等键、模型标签不符合原生 CompactionSession 的 OpenAI 名称校验，分别修正测试后通过；保留诊断日志，没有放宽生产请求或 Session 校验。此联合测试仍是确定性模型，不是外部网关验收；主对话多审批/失败分支的完整联合矩阵也不能由本例替代。G4.5 的 542 项保留其历史 SDK 分层含义，本批 Python 仅新增集成 fixture，未改 Python 生产模块。
- 仍处阶段 1，GAP-15 未完成：主对话/状态化当前执行追加、不可变输入记录与收件回执、附件/二进制、撤回修订、失败后安全继续及三模式真实联合验收仍待完成。未更新或访问 8860/8880、迁移用户数据库、改变模型/网关/凭据、安装 OCI、push、部署或操作测试机；最终独立全局 review 尚未开始。

### G4.7a 主对话追加输入的不可变记录与调度回执基础

- 源码 schema 46 新增 `agent_turn_inputs`，为每条输入持久保存作者、顺序、正文和 received/included 状态；原始 MessageRequest、轮次身份、首次开始时间与冻结 Skill 不被追加改写。单条 32 KiB、每轮 128 条 / 512 KiB，按 UTF-8 字节校验；记录、生命周期事件和命令幂等在同一事务提交。本人当前写权限、其他作者、跨工作区、Agent 工具身份和终态均有门禁；原命令迟到重试返回原输入及最新收件状态，不重复追加。
- 每次 Go 领取同一轮时递增私有 `dispatch_generation`，在同一事务绑定当时已经接收的完整输入快照。回执只能确认本次调度已领取、有序且无重复的输入前缀；旧调度、其他输入、乱序、重复和本次领取之后才到达的输入均拒绝，非法回执不能连带保存 checkpoint。已确认输入保留首次 included 时间，重复回执不重复写事件；正文不进入生命周期事件，调度代次与自动暂停标记不进入公开 JSON。
- 运行中追加请求原生暂停所需的 Go pausing 状态，独立记录自动继续意图。只有无旧审批调用的 checkpoint 才自动排回原轮；手动暂停覆盖此意图，继续保存 paused。追加遇到等待审批或已批准但尚未执行的旧 checkpoint 时保持人工暂停，必须显式继续；批准/拒绝本身不覆盖暂停。manager 保存主对话 checkpoint 时附带领取代次，拒绝旧执行覆盖新调度状态。
- checkpoint 已提交后重建 Go 可继续原轮并重新领取输入；未提交 checkpoint 就中断时仍按既有策略失败关闭，不从头重跑，也不伪造模型收件。新增用例覆盖并发同键请求、唯一连续序号、终态前迟到追加、回执幂等、自动/手动暂停重建、批准/拒绝的三种交错及 v45 -> v46 迁移，核对原生 JSON 精度、冻结目录、开始时间和迁移备份。
- 首轮主对话暂停/输入专项 `67.359s` 通过，`.tmp/goal-g47-turn-input-runtime.log`。补并发与重建用例后，主对话、后台追加和旧版本迁移等扩展回归 `135.664s` 通过，`.tmp/goal-g47-turn-input-boundaries.log`；HTTP 全包 `101.105s`、shell 全包 `2.205s` 通过，`.tmp/goal-g47-turn-input-http-shell.log`。
- 原有 Go + 真实 SDK 主对话写文件/暂停/重建/继续联跑重新通过，包耗时 `35.544s`，`.tmp/goal-g47-sdk-main-pause-regression.log`；每阶段模型请求 1 次、原 Session 用户输入 1 份、文件版本 1，仍为确定性模型。`go test -p 1 ./... -run '^$'` 全包编译通过，`.tmp/goal-g47-go-compile.log`；这是编译检查，不是本批 Go 全量回归。所有本批测试命令已退出，涉及 Go 文件 gofmt 与 diff 空白检查通过。
- **本批仅完成主对话追加的 Go 内部基础，不是用户功能已可用。** 尚未开放追加 HTTP API/UI，manager 的输入快照向 Sidecar 透传、原生 add_input、旧审批处理后追加、SDK 收件证明及前端 received/included 还需接通；`RecordAgentTurnInputsIncluded` 和 checkpoint 输入回执目前由 Store 测试验证，没有把 Store fixture 调用算成模型已经收到。Python 生产、前端及运行环境本批未改，未以既有暂停联跑冒充追加联跑。
- 仍处阶段 1，GAP-15 和其他原承诺保持未完成。未访问或更新 8860/8880、迁移用户数据库、改变网关/模型/凭据、安装 OCI、push、部署或操作测试机；完整三模式真实验收和最终独立全局 review 未开始。

### G4.7b 主对话追加的 SDK、用户 API 与前端闭环

- 公开 `POST /api/v1/agent-turns/{id}/inputs` 已接入原作者写权限、严格正文解析和持久幂等；manager/shell 仅透传本次领取时冻结的追加快照，不把后来收到的输入假装为当前模型上下文。原请求、会话和轮次身份不变。
- Sidecar 使用固定 SDK `RunState.add_input`、原生 after_turn 暂停与原 Session，保存已暂存/已送入输入的有序 ID 和内容哈希。恢复核对输入身份、内容、顺序、原生 pending_input 与持久回执；零模型初始输入和修复阶段继续保留 G4.5 的 guardrail/Session 边界，没有重建会话历史或新增模型循环。
- 对自定义提交终止行为，先在原生 after_turn 边界按原决定处理旧审批，且不请求新模型；若旧工具提交终态，不追加也不伪报新输入生效。非终态再由原生 add_input 接入新要求。追加不撤销旧批准，也不能回滚已发生的写入；未决审批不会因新要求自动获准。
- 只有本次真实 response.completed、正常排空 SDK 流且原生 pending_input 已消费，才输出模型收件 ID。零模型暂停、未决审批、模型失败或旧工具提前结束均不生成新收件证明。暂停/审批回执与私有 checkpoint 在同一事务落库；最终提交后回执另外持久化，响应丢失可能保守保留 received，不将提交本身当作收件证明。空回执省略新字段，维持原有事件契约。
- 主对话新增追加图标、草稿、失败同键重试和 received/included 明确回执；新增 SSE 事件按原严格项目协议刷新。原作者/当前写权限门禁、其他作者只读、暂停中“保持暂停”、历史回执和终态草稿保留已接通。终态及权限撤销禁用提交，未提交草稿仍可重新打开；草稿仅保留在当前组件生命周期内，不承诺刷新、切页或卸载后持久保留。
- 前端完整回归 `278 passed`，`.tmp/goal-g47b-frontend.xml` 回读 0 失败/错误；TypeScript 与 `.tmp/goal-g47b-frontend-dist` 隔离构建通过。桌面 1440x900 / 手机 390x844 验证追加失败保留、同键重试、刷新回执、SSE 收件、只读门禁、终态草稿及长文本，无横向溢出，图标 36x36，截图已目视检查。报告 `.tmp/goal-g47b-main-input-ui/report.json` 明确模拟 API、modelCalls=0、userBackendAccess=false；临时 Vite/API/浏览器已关闭。
- HTTP/shell 首轮仅新 HTTP 测试把既有 `IDEMPOTENCY_KEY_REUSED` 的 400 写成 409 而失败，`.tmp/goal-g47b-http-shell.log`；未修改生产错误映射，修正测试并显式断言错误代码。最终全包重跑退出 0，HTTP `109.768s`、shell `1.545s`，`.tmp/goal-g47b-http-shell-verified.log`。
- Go HTTP + 实际 Sidecar/SDK Runner/SQLiteSession 跨进程通过原暂停回归、追加后手动暂停/关闭重建/显式继续、自动暂停继续和完成前迟到输入三项。核对同一轮、原 Session 用户项恰好新增一份、旧文件写入仍为版本 1、旧工具不重放；自动分支第一条已 included、最后迟到条仍 received。初次联跑 `30.010s`，最终重跑 `42.329s`，`.tmp/goal-g47b-sdk-main-input-final.log`，均退出 0。模型为确定性 fixture，不是外部网关模型，也不替代主对话完整审批/失败和三模式联合矩阵。
- SDK 完整回归初次 `561 passed / 1 failed`，`.tmp/goal-g47b-sdk-full.xml`，暴露空 `included_input_ids` 改变既有精确事件契约，已改为无回执时省略字段。默认权限重跑在 Windows pytest 临时目录访问/清理被拒，日志 `.tmp/goal-g47b-sdk-verified.log`，不当作业务通过；未修改 ACL，经权限审查在新隔离目录最终全量重跑退出 0，`566 passed in 93.57s`，`.tmp/goal-g47b-sdk-reviewed.xml` 回读 0 失败/错误/跳过。包含恢复后新输入被 guardrail 拒绝、旧批准工具可能先执行/拒绝不执行、模型不调用、Session 不保存被拒输入及不伪报收件，以及持久 included 与原生账本不符的拒绝测试。
- 本批所有测试命令均已结束；Go 新测试 gofmt、最终 diff 空白检查通过。此次没有重新运行 Go Runtime 全包，G4.7a 的 Runtime/迁移专项和 G4.6 的全包结果保留各自历史含义，不拼接宣称本批 Go 全包一次全绿。
- 本批仍处阶段 1，源码 schema 46 未继续变化，GAP-15 保持未完成。状态化追加、附件/二进制、撤回修订、失败后安全继续、完整三模式真实验收及其他承诺仍须完成；原生压缩网关和真实 OCI 验收条件仍未解除。没有访问/更新/重启 8860/8880、迁移用户数据库、改变模型/网关/凭据、安装 OCI、push、部署或操作测试机；最终独立全局 review 未开始。

### G4.8 状态化生成的原生暂停与原尝试恢复

- 核对后确认，既有 Run 暂停主要等待整个任务结束或业务游标边界，不能把它算作正在执行的 SDK 中间状态已可保存。本批为实际 SDKTaskWorker 的生成阶段接入原生 `cancel(mode="after_turn")`：通过现有 heartbeat 回执的 `pause_requested` 通知，排空原生流后保存 RunState、Worker 阶段和已完成源分析批次；不另建模型循环或执行器。
- 复用 `execution_run_states` 表及无 CHECK 约束的既有 attempt/task 状态列，增加原生 paused 状态及内部 `pause-checkpoint` 路由。schema 仍为 46，无新增迁移。无审批 checkpoint 不冒充 waiting_approval；普通 approval-checkpoint 仍拒绝空审批列表。保存时校验服务身份、令牌、委托用户权限、输入哈希、SDK schema、工具运行/审批集合和大小限制，私有状态不进入公开回执或事件。
- 保存原生暂停后保留 task.current_attempt_id、attempt ID/次数/开始时间和冻结 ContextPack。多个活动兄弟任务全部到达边界后 Run 才 paused；继续后只重新领取同一次 SDK 尝试并轮换令牌，不把原生暂停任务重设为全新 pending。旧令牌、缺失/损坏 checkpoint、权限撤销、取消和删除均不能重新执行旧操作。运行中的暂停撤回保留原语义，已收到信号的 Worker 仍可保存原生状态，然后由仍 running 的 Run 重新领取恢复。
- 有旧工具审批时，用户暂停保留授权；批准/拒绝不自动继续。显式继续但仍有未决工具授权时，原 attempt/task 转为 waiting_approval，前端准确显示等待授权，领取条件仍拒绝执行未批准工具。暂停保存回执丢失并遇到用户已继续/等待授权时，可确认原持久回执；令牌已轮换则停止旧 Worker，不报告可重试 provider 故障。已完成最终输出允许胜出，不保存伪非终态 checkpoint。
- 初次模型请求前暂停沿用零模型严格边界，保存精确渲染输入；恢复时核对 SDK original_input、冻结输入、阶段和空工作状态后进入第一次原生 Runner，让初始 guardrail 仍生效。已执行状态仍恢复原 RunState。连续零模型暂停、已完成文件工具、批准/拒绝、完成竞争和 81 个源单元的已完成批次及用量经实际 SDK 测试；不把已完成批次再次交给模型。
- 输入拒绝回归真实复现了既有错误分类：`InputGuardrailTripwireTriggered` 被归为 `PROVIDER_TEMPORARY_FAILURE`。已与后台一致改为 `AGENT_INPUT_GUARDRAIL_REJECTED`、input_validation 阶段且不可自动重试，并接 Go 失败代码白名单/持久记录。初始与两次零模型暂停后的拒绝均不发新模型请求；失败复现报告 `.tmp/goal-g48-guardrail-before.xml` 为 2 失败/2 通过，保留证据。
- 前端沿用原 Run 暂停/继续按钮、任务进度和审批卡，新增 `task.paused` SSE 刷新及卸载监听回归；无新布局。默认 Vitest 启动受 Windows spawn EPERM 阻止，经权限审查重跑完整 `279 passed`，`.tmp/goal-g48-frontend-reviewed.xml`；TypeScript 与 `.tmp/goal-g48-frontend-dist` 隔离构建通过。本批未新增浏览器截图，不用上一批模拟 API 截图冒充当前真实状态化交互验收。
- Sidecar 初次专项为 49 通过/1 新测试失败，原因是测试未计源分析既有的归一化字段；修正后完整回归为 573 通过/2 旧测试替身失败，补齐原生流已有的 cancel 方法后 575 项通过。随后补未决审批回执及输入失败分类，最终完整 `578 passed in 128.48s`，`.tmp/goal-g48-sdk-verified.xml` 回读 0 失败/错误/跳过。不放宽生产 SDK 契约或以替身冒充真实 Runner。
- Go 初次状态化/续租/暂停专项 `94.479s` 通过；补多个兄弟任务、未决审批继续及回执后 `157.268s` 通过，`.tmp/goal-g48-stateful-boundaries.log`。全包 `.tmp/goal-g48-go-all.jsonl` 退出 1：Runtime `623.272s` 与其余有测试包通过，HTTP 仅新暂停 helper 复用旧 resume 幂等键而得到旧回执，不能宣布该全包命令通过。给新操作独立幂等键后 HTTP 该流程 `7.566s` 通过，最终 HTTP 全包 `87.880s` / shell `2.702s` 通过，`.tmp/goal-g48-http-shell-verified.log`。全包编译时间早于末尾未决审批/输入拒绝修正，末尾专项及跨进程复验另行保留，不拼接为同次全包全绿。
- 真正 Go HTTP + Python SDK Worker 联跑扩展到写文件后原生暂停、关闭重建 Go/Worker、公开 resume、继续校验与安装审批、再次重建后公开批准/拒绝，核对原 attempt、文件版本 1、工具不重放、累计用量和正式产物。中途 fixture 依次修正了暂停接口 202、Run 与 RunSnapshot 响应类型和过低的 50ms HTTP 续租超时；保留失败日志，不改生产契约。阶段性原作者/新暂停共四分支 `83.540s` 通过，`.tmp/goal-g48-sdk-stateful-http-verified.log`；末尾审批/输入错误分类后的最终复验 HTTP `71.834s`、Go 失败代码持久化专项 `13.313s` 均退出 0，`.tmp/goal-g48-sdk-guardrail-final.log`。
- 所有本批测试命令均已结束，相关 Go 文件 gofmt、最终 diff 空白检查通过。未将初次 Go 全包的 HTTP 失败、fixture 失败或权限阻止记作通过，也未因此重跑任何用户作品。
- **仍是阶段 1 的状态化追加前置能力，不是状态化追加已交付。** 当前原生暂停覆盖 SDK 生成阶段与源分析批次；输出修复、后端拒收后的修复以及视频专用执行仍沿用既有任务边界，未把它们宣称为可在中途原生恢复。紧接补这些阶段的恢复、状态化不可变追加账本与回执、输入范围和用户入口，继续附件/二进制、撤回修订、失败后安全继续、其他 Skill 路由/依赖及完整三模式真实验收。GAP-11/15 保持未勾选，最终独立全局 review 未开始。
- 未访问/更新/重启 8860/8880、迁移用户数据库、改变模型/网关/凭据、安装 OCI、push、部署或操作测试机；所有临时 HTTP 联跑使用独立随机端口，模型为确定性 fixture，不是外部模型或前端三模式同场验收。原生 Responses compaction 网关和真实 OCI 验收条件仍未解除，不因此缩小完整目标。

### G4.9 状态化输出修复的原生恢复与正式提交拒收缺口（2026-09-07）

- 状态化 Worker 本地输出解析/合同修复已从独立非流式 Runner 接入同一原生流式暂停路径。私有 `stateful_worker.v2` 区分 generate、repair_parse、repair_validate、repair_backend，保存精确候选、校验错误、此前已发生用量、trace 和对应冻结 pack/源分析分块的 SHA-256；不改写原 ContextPack 或 SDK RunState。旧 v1 生成 checkpoint 继续可恢复。SQLite schema 仍为 46，无用户数据迁移。
- 恢复本地修复时跳过主生成、工具准备和材料重新渲染，直接重建原修复 Agent/RunState；零模型状态仍核对原输入并执行初始 guardrail，已执行修复保留原生 turn 预算。连续暂停、JSON 兼容模式、候选/输入/身份/阶段/用量损坏、丢失保存回执/令牌轮换、81 单元批次的已完成结果和当前修复均有实际固定 SDK 回归。视频专用任务仍不接受文本 SDK checkpoint，未改视频 Prompt 或其专用执行边界。
- 修复原先本地格式修复替换 result、丢掉主生成用量的问题；兼容修复失败中 SDK 已返回的 RunErrorDetails 用量也累入阶段记录，完成修复后只加一次原生恢复结果的累计用量。合并时保留已有用量附属字段和权威上下文读取 ID；不声称能推算提供方未返回的计费数据。
- **新确认的 P1 缺口，仍未完成：真正 Go 拒收发生在 CommitExecutionResult，而非 SubmitExecutionResult。** 当前 `task_worker.py` 的拒收捕获围绕 `/results`；Go `executor.go` 的 SubmitExecutionResult 只保存结果并置 result_received，业务/Schema 校验在 `artifact_commit.go` 的 CommitExecutionResult。`run_once` 在退出执行续租范围后调用正式 commit，拒收直接进入失败路径；同一 attempt 的不同 response_hash 又被 ATTEMPT_RESULT_CONFLICT 拒绝。因此本批 repair_backend 仅完成 Sidecar 既有上传拒收分支的阶段/上下文接线及确定性 Backend 测试，**不能宣称实际正式提交拒收已会自动修复**，也不能把这类替身测试算作 Go 端到端验收。紧接实现权威拒收记录、受控候选修订/领取和原 attempt 修复恢复，不靠重新生成主任务或覆盖旧回执绕开。
- 实际 SDK 回归复现输出安全 guardrail 拒绝被归为 PROVIDER_TEMPORARY_FAILURE，`.tmp/goal-g49-guardrail-before.xml` 为 1 失败/1 通过；已接 `AGENT_OUTPUT_GUARDRAIL_REJECTED`、output_validation、Go 非重试失败白名单及数据库重建后持久记录。同时 OUTPUT_LENGTH_INSUFFICIENT 不再误归临时提供方故障；输入拒绝提示改为“未继续发起模型请求”，避免在修复阶段抹掉之前真实发生的生成。
- 最初 `.tmp/goal-g49-before.xml` 2 失败：用量测试的模型 fixture 当时只支持流式，旧非流式修复先触发 fixture 类型错误，不能把该次失败当作独立用量损失实测；另一项复现暂停信号后仍请求修复模型。随后默认权限完整专项遇到 pytest 临时目录 PermissionError，`.tmp/goal-g49-focused.log`，不计为通过；经审查新目录 `.tmp/goal-g49-focused-reviewed.xml` 为 123 通过/8 失败，均为测试边界更新：续租 fixture 缺少现在真实进入的 activity.project_id，新断言把 SDK 标准化 input list 当成 string。修正 fixture，不放宽生产身份/输入校验；修复专项 36 项及分批专项 27 项通过，各自报告保留。
- Sidecar 阶段性完整 605 项通过后加入输出拒绝/旧 checkpoint/长度拒绝回归，最终 `.tmp/goal-g49-final-reviewed.xml` 为 **608 passed in 100.09s**，回读 0 失败/错误/跳过；使用经审查的新隔离目录，不修改 ACL。所有模型均为确定性测试模型，不调用外部网关。
- 新 Go+实际 Python Worker 联跑覆盖写文件后修复前零模型暂停、修复已发生一轮后暂停、关闭重建 Go/Worker、公开继续、原 attempt/次数/开始时间/冻结输入/累计用量、文件版本仍 1、工具不重放、正式产物提交，以及重建后输入/输出 guardrail 拒绝。首轮仅新 Go 测试字段误写 `file.Version` 而编译失败，`.tmp/goal-g49-sdk-repair-http.log`；修正为已有 `file.File.Version` 后原作者/暂停审批和新增修复三分支 `113.296s` 通过。加入输出拒绝后的最终 `.tmp/goal-g49-final-http-runtime.log` 为 HTTP 专项 **142.820s**、Go 非重试失败持久化专项 **16.779s**，命令退出 0。该联跑不包含上述真实正式提交拒收修复，亦非外部模型或前端三模式同场验收。
- 本批未改前端或重新跑前端/Go 全包，不挪用旧报告宣称同版全量通过。所有测试命令已结束，相关 Go 文件 gofmt、最终 diff 空白检查通过。仍是阶段 1，GAP-11/15 不勾选；状态化追加、正式提交拒收修复、视频边界、附件/二进制、失败安全继续、其他原承诺及真实三模式验收继续保留，最终独立全局 review 未开始。
- 没有访问/更新/重启 8860/8880，没有用户数据库迁移、修改网关/凭据/模型、安装 OCI、push、部署或操作测试机；原生 Responses compaction 和 OCI 外部验收条件仍未解除。

### G4.10 正式提交拒收的持久修复通道（分项隔离验收通过，2026-09-07）

以下前五条保留本批起始状态；之后的收口记录更新当前结论，不将早期失败抹去。

- 已在实际 Go `CommitExecutionResult` 复现业务/Schema 拒收不会进入修复，失败日志 `.tmp/goal-g410-commit-rejection-before.log` 保留。源码 schema 47 新增不可变拒收记录；显式 SDK 修复请求保留原候选、哈希、用量和错误，转入 `repair_pending`，重新领取同一 attempt 并轮换令牌。修复阶段禁止重新调用业务工具，第二次拒收终止，租约恢复有界，不以新建主生成或覆盖旧记录规避拒收。仅测试临时数据库使用新 schema，用户数据库未迁移。
- Python Worker 已接 Go 拒收记录到 `repair_backend`，恢复时核对候选、输入、来源和用量；原生暂停保留修复阶段，上传/提交回执不确定时保留已持久结果，不直接报提供方失败并重生成。当前证据为 Go Store 专项和实际 SDK Runner 配合确定性 Backend 的分层测试，**本批真实 Go HTTP + Python Worker 联跑尚未完成**，不能宣布该通道端到端交付。
- Go 暂停/恢复、取消、删除标记门禁、权限、损坏候选/用量/缺记录、模型过滤、第二次拒收、租约上限与已接收结果恢复专项通过，`.tmp/goal-g410-runtime-boundaries.log` 为 `21.014s`。补实际 heartbeat 必须返回已接收结果哈希的断言后重跑通过，`.tmp/goal-g410-receipts-runtime.log` 为 `23.485s`。删除用例目前仅直接设置项目删除标记，不冒充公开项目/工作区删除流程验收。
- Python 首轮默认权限受 pytest 临时目录 WinError 5 阻止；审查后 `.tmp/goal-g410-sdk-reviewed.xml` 为 73 通过/2 失败，定位为测试替身已持久结果的 heartbeat 缺少真实 Go 返回的 `response_hash`。补齐替身并在 Go 加断言，未放宽生产校验；中间复验为 74 通过/1 失败，新增过宽的全事件循环任务断言误计 SDK HTTP 客户端析构清理，改为本用例关心的精确 commit 次数断言，既有续租清理专项保留。最终 `.tmp/goal-g410-sdk-receipts-final.xml` 为 **75 passed in 8.93s**，XML 回读 0 失败/错误/跳过；不是本版 Sidecar 全量回归。所有上述命令已退出，Go 测试文件 gofmt 和 diff 空白检查通过。
- **仍处阶段 1，G4.10 未完成。** 接下来核对修复领取时冻结输入损坏是否阻塞其他任务、用量与领取次数边界，补 schema 47 迁移、正式 HTTP 权限/回执/公开删除测试和真实 Go+SDK 联跑，再接前端修复状态并扩大回归；本批尚未改前端、运行 Go 全包或进行三模式真实模型验收。状态化追加、附件/二进制、失败安全继续、MCP 凭据、沙箱、协作/规则等原缺口仍保留，最终独立全局 review 未开始。未访问或操作 8860/8880、用户作品、网关/模型/凭据、测试机，也未安装 OCI、push 或部署。

#### G4.10 收口记录

- Go 拒收记录继续保持不可变；领取时验证冻结 ContextPack、候选和用量哈希、累计用量不得倒退以及三次领取上限。坏记录在所属任务终止，不阻塞其他 Run。恢复队列跨过整页不匹配模型、每个候选重新检查 Run/Step 状态；批次遵守既有 preserve_success_retry_failed 策略，非批次致命失败后不领取同 Run 其他任务。结果恢复仅对 openai_agents_sdk Provider 开放，不改变旧 Provider 的 result_received 协议。实际产物写事务内再次核对执行令牌和作者权限，关闭外层预检与写入间的授权竞争窗口。
- 已补 schema 46 到 47 的备份迁移、候选/开始时间/冻结输入保留，以及公开 Store 项目删除预览/确认和工作区删除命令对修复尝试的取消。它们是实际 Store 命令测试，不冒充公开 HTTP 删除验收。正式 HTTP 覆盖服务认证、缺失或错误 attempt token、opt-out 兼容、幂等回执、令牌轮换后旧请求拒绝和任务列表只返回公开修复摘要、不泄漏原候选/令牌/错误详情。
- `sdk_stateful_result_repair_integration_test.go` 与实际 Python Worker 联跑：首次模型结果符合 Schema、但覆盖来源标识错误；实际 Go CommitExecutionResult 返回 BATCH_COVERAGE_INVALID 并排入原 attempt 修复。分别验证修复成功、原生暂停后关闭重建 Go/Worker 再公开继续、第二次实际业务拒收终止。重复 commit 返回原回执；次数/开始时间/冻结输入不变，已写文件仍为版本 1、工具只调用一次，累计用量保留。`.tmp/goal-g410-final-contract-http.log` 命令退出 0，Runtime 专项 61.787s，HTTP 专项 79.132s。成功终点是源分析正式 task_checkpointed，不是整个业务流程完成；模型是确定性 fixture，不是外部模型。
- 原先队列新测试误将源分析批次当作整 Run 立即失败，`.tmp/goal-g410-queue-final.log` 保留该失败。修正测试为真实安装的非批次 Skill，并另加批次兄弟继续/全部结算后失败用例，没有改变业务失败策略。新四项 `.tmp/goal-g410-queue-policy.log` 19.349s 通过；扩大修复/收件/队列专项 `.tmp/goal-g410-final-policy-runtime.log` 70.475s 通过，两条命令退出 0。
- Sidecar 全量 `.tmp/goal-g410-sdk-all-reviewed.xml` 为 618 通过、0 失败/错误/跳过，日志 143.21s，退出 0。Go 较早全包 `.tmp/goal-g410-go-all.jsonl` 已回读全部包终态：13 个有测试包通过，4 个无测试包跳过，无失败；Runtime 601.697s、HTTP 121.848s。原会话句柄因中断丢失，不虚构 shell 退出码；该全包早于最终写事务授权及 Provider 限定修正，最新代码使用上述专项回归，不能拼成同版全量通过。
- 前端已展示等待修复、正在修复结果、修复已暂停、正在校验结果，并沿用原有暂停/继续入口。公开 TaskItem 仅投影 output_repair 状态/拒收次数/错误码，原 attempt 修复不显示为重新生成重试。类型检查及隔离 Vite 构建退出 0；最终完整 Vitest 使用已安装版本支持的 threads 单 Worker，`.tmp/goal-g410-frontend-threads.xml` 为 37 文件、288 通过、0 失败/错误。默认权限 Vite spawn EPERM、fork Worker EPERM 的失败日志保留，未修改 ACL 或测试业务断言绕过。
- 实际完整 App 配合隔离 mock API 的桌面 1440x900/手机 390x844 交互通过：等待、修复、暂停、刷新、继续、校验、失败；无横向溢出、状态重叠或不应出现的重试按钮，截图已检查。报告 `.tmp/goal-g410-repair-ui/report.json` 标明 modelCalls=0、userBackendAccess=false、errors=[]；截图在同目录，最终日志 `.tmp/goal-g410-repair-ui-final.log` 退出 0。首轮缺 active_write_run_id 与第二轮按钮高度断言不匹配现有 CSS 的 fixture 失败仍保留，未修改产品布局掩盖问题。此 UI 验收与 Go+SDK 联跑是分层证据，不是三模式真实模型同场 E2E。
- **仍处阶段 1，GAP-11/15 保持未完成。** G4.10 的实际正式提交拒收缺口已补，紧接状态化当前执行追加账本/原生消费/模型回执/API/UI，不再重复补本地修复或正式拒收通道。附件/二进制、撤回修订、失败后安全继续、MCP 凭据、沙箱、协作/规则等原范围保留；完整三模式自测回归和最终独立全局 review 尚未完成。未访问/操作 8860/8880、用户数据库/作品、模型/网关/凭据、测试机；未安装 OCI、push 或部署。

### G4.11a 状态化追加的 SDK 原生输入层（内部基础通过，2026-09-07）

- 新增 `stateful_inputs.py` 并接实际 `StatefulExecution` / `SDKTaskWorker`，使用固定 SDK 0.21.1 的公开 `RunState.add_input`，不改 SDK 内部实现、不改 ContextPack 原输入或哈希。输入必须带确切 attempt_id、连续 sequence、唯一 input_id、内容 SHA-256 和 received/included 状态；每条 32 KiB、每次尝试 128 条/512 KiB，身份、次序、内容或既有记录被替换时拒绝恢复，不重新生成规避校验。
- 有追加时保存私有 `stateful_worker.v3.input_ledger`；无追加继续写 v2，v1/v2 生成恢复仍兼容。保存当前阶段原始输入前缀、原生 pending 输入前缀和模型消费前缀：固定 SDK 将后续 add_input 内容保存为 generated InputItem，不改 original_input，因此原修复候选核验与后续追加分别校验。零模型再次暂停不伪造模型消费；新增要求通过原生输入 guardrail 后、收到模型 response.completed 且 native pending 为空才确认。最终输出 guardrail 拒绝不抹掉已发生的模型消费，输入 guardrail 拒绝则保持未消费。
- 本地修复、Go 拒收修复的 Worker 入口，以及源分析分批均已接追加；新阶段继续携带仍适用的已登记要求，不重放之前工具、不重做已完成批次，不增加原生剩余 turn 预算。已批准/拒绝的旧工具仍走 SDK 原有处理次序，追加不撤销旧授权或已发生副作用。视频专用执行收到文本 SDK 追加显式拒绝，不假称完成视频中途追加支持。
- 新测试覆盖连续追加、旧审批批准/拒绝、原生 pending 后零模型再暂停、初始及 pending-input guardrail、输出拒绝的模型收件、内容/顺序/哈希/原始输入/checkpoint 版本篡改、本地/正式修复阶段恢复，以及 81 单元三批保留已完成第一批。第一轮 `.tmp/goal-g411-sdk-first.xml` 为 72 通过/1 失败，新批次断言漏掉现有 source_refs 标准化；改为对比暂停时完整已保存批次，未改生产标准化逻辑。扩大专项 `.tmp/goal-g411-sdk-focused.xml` 为 76 通过；追加收件异常边界后专项 `.tmp/goal-g411-receipt.xml` 为 26 通过。默认缓存写入权限警告保留；完整测试使用审查过的新隔离目录，未修改 ACL。
- 较早完整 Sidecar `.tmp/goal-g411-sdk-all-reviewed.xml` 为 642 通过；补输入/输出拒绝收件后最终 `.tmp/goal-g411-sdk-final-reviewed.xml` 为 **644 passed in 83.54s**，XML 回读 0 失败/错误/跳过，命令退出 0。所有模型为确定性 fixture，不连接外部模型。
- G4.10 最终 Provider 限定后的真实正式拒收/认证联跑 `.tmp/goal-g410-http-final-both.log` 37.024s 通过。G4.11 新输入层后的既有状态化 Skill 作者、原生暂停审批、本地修复与正式拒收 Go+Python SDK 联跑 `.tmp/goal-g411-stateful-http-regression.log` 152.418s 通过，命令退出 0；这些 HTTP 测试没有追加输入，是原功能兼容回归，**不是新追加功能的 Go HTTP 端到端验收**。新增追加测试为实际 SDK Runner 加确定性 Backend 替身，不能混称 Go 持久回执已接通。
- **G4.11 尚未完成，用户追加入口未开放。** 当前消费记录仅在 Worker 内存/私有 checkpoint 中；Go 尚无状态化追加不可变账本、按当前领取代次绑定及持久模型回执，也未接用户 API/SSE/前端。源码数据库仍为 47，本批未再迁移。下一步围绕确切当前 attempt 实现持久输入与领取代次，区分自动输入暂停、Run 人工暂停、旧审批待显式继续、迟到输入与正式结果提交竞争；同时让成功、暂停及失败都保存正确消费事实，再接前端目标范围和 received/included/未纳入结果状态。不得用下一轮排队、取消重试或修改冻结上下文替代追加。
- 本批全部测试命令已结束，diff 空白检查通过；未改/访问/重启 8860/8880、用户项目/数据库、模型/网关/凭据、测试机，未 push、部署或安装 OCI。Goal 面板在用户本轮“继续”时仍显示 paused；本轮按明确指令继续开发，但没有伪称程序化恢复自动 goal，也没有将目标标记完成。仍处阶段 1，其余全部原承诺保留，最终独立全局 review 未开始。

### G4.11b 状态化追加的 Go/API/UI 闭环与复核（2026-09-07）

- schema 48 增加不可变 `execution_inputs`，绑定当前领取令牌、原 attempt/输入哈希和模型已消费前缀；新增严格用户写 API 与内部收件 API。原生 after_turn 自动暂停后继续原尝试，审批中的追加不隐式批准或启动旧工具；迟到输入不假报 included。
- 补前端当前处理项选择、追加、幂等键与草稿保留、权限失败、完成状态和 SSE 回执。已有草稿时不允许切换处理项，重试创建新 attempt 时不默默转移旧草稿。
- 首轮 Runtime 5 项通过 26.413s；迁移 47 -> 48、8 个并发同键重试和取消边界扩展通过。真实 Go + 固定 SDK + 确定性模型跨进程三分支通过 34.126s，原 attempt、原工作文件仅一版、累计请求次数均有断言；不是实际网关验收。
- 前端全量 298 项、类型检查和隔离构建通过；真实 App + mock API 的桌面/手机新增入口、收件、暂停/刷新/继续通过，截图已查看。Sidecar 在后续按需发现修改前全量 644 项通过 120.397s。
- 原 G4.11a 的“Go/API/UI 尚未接通”已由本批替代，不应再次从追加基础开始。文本追加分项通过不等于附件追加、输入撤回或三模式同场真实模型验收完成。

### G4.12 跨模块 Review 修复与门禁复核（2026-09-07）

- 首次领取前 100 项过滤顺序导致的饥饿已修复；新增第 102 项可领取、前面不适用项保持不变的测试。恢复队列既有修复不代替首次领取。
- 三种追加命令在写事务内复查用户/工作区和编辑权限，拒绝认证后发生的撤权；后台幂等重试读取当前收件而非过时缓存。
- 已连接冻结目录中未预路由的 inline Skill 按需加载，复用动态依赖/SDK 工具准备，不扩展安装范围或审批权限。新增 12 项 Provider 与实际主 Runner 的发现/审批/重建测试全部通过；相关 SDK 串行专项仍有 3 个既有 MCP 场景失败，不算完整回归通过。
- 新报告 `agent-platform-global-review-20260907.md` 逐项区分已修问题、能力欠账和外部条件。完整原生工作区、MCP 用户凭据、MCP 文件交付、协作/规则等没有被悄悄移出原范围。
- 最终全量重跑遇到 Windows Application Control 拦截及多项 stdio MCP 超时。已有通过证据保留，但不得把此前不同代码版本的通过结果合并成最终全绿，也不修改系统策略或 ACL 绕过限制。
- 收尾：新增 Skill 发现 12 项全部通过；相关 SDK 串行专项 82 通过/3 个既有 MCP 场景失败。三模式追加 Runtime 专项通过，首次领取及原有视频顺序回归最后 18.355s 通过，新增 HTTP 权限与 Go+SDK 追加三分支最后 130.644s 通过；UI 最终重跑通过。完整 Go 仍有 Application Control 拦截/总时限超时，完整 tagged SDK HTTP 亦未全过。所有所需命令已返回终态，详细证据和欠账保留在 review 报告，未宣称原目标完成。

## 紧接工作

### G4.13 MCP 文件交付增量（2026-09-07）

- 之前失败的 Skill 作者安装/二次 MCP 审批三场景，在未修改生产超时或系统策略的串行复测中通过；主对话及后台相关 38 项通过，34.20s，`.tmp/goal-g413-author-mcp.xml`。此前失败不抹除，不能据此宣称全量稳定性已验收。
- MCP 图片、音频、嵌入文本/二进制资源、资源链接已接项目资产存储。链接仅由当前已授权 MCP 会话 `read_resource` 读取，不把 URI 当宿主机路径、不发独立任意 HTTP 请求。限制文件数、总字节、资源身份和完整状态；全部输入预校验后才持久化。SDK 原生内容保留，并加入绑定真实资产/快照的回执；审计摘要不复制图片/音频/文件的内联字节。
- Go 复用现有资产配额、MIME 验证、幂等、生命周期和事务身份检查，新增明确 `mcp_tool` 来源。前端沿用执行记录的下载/再次作为材料入口，按项目、工具种类、平台调用及 SDK 调用精确匹配，不信任模型返回的任意文件 ID。
- Python 最终相关 56 项通过，37.76s，`.tmp/goal-g413-output-python3.xml`。包含真实 stdio + SDK 工具的 inline/link/image/error × 三执行身份，以及超限、非法 base64、空/身份变化资源、未完成结果、跨项目/调用/快照回执拒绝。初轮新 fixture 不支持联合返回类型的失败已修正为独立 fixture，未修改原故事 fixture 或 SDK。
- Go 存储及 HTTP 专项通过，14.297s / 13.341s，`.tmp/goal-g413-output-go.log`。新增真实 Go HTTP + SDK Runner + 本地 stdio MCP 三身份联合测试通过，35.001s，`.tmp/goal-g413-output-go-sdk-final.log`。每种身份 4 次确定性模型响应：CSV、资源链接、图片和错误；重复上传保持同资产/快照，Go 重建后公开下载字节不变，跨工作区/未登录拒绝，错误结果无资产。后台 fixture 初次遗漏 Skill invocation 身份导致拒绝，已补正确身份，未放宽生产校验。
- 前端专项 19 项、类型检查通过；1440x900 与 390x844 的下载、复用、禁止删除/归档材料、精确快照发送通过，截图已检查。`.tmp/goal-g413-mcp-output-ui/report.json` 明确为 mock API UI。首次浏览器 EPERM 后经审查运行，旧分组文案 fixture 已更新，最终命令退出 0，测试服务关闭。
- 本批仍非完整 Worker 用户任务或真实外部模型全模式 E2E；上述模型为确定性模型，UI 另用 mock API。未知二进制仍沿用 ZIP 下载且不可直接作材料，文件数/字节限制保留。原生工作区/Shell、用户 MCP 凭据、通用协作/规则、其余输入与安全继续、全量回归和最终 review 仍在原 Goal 内，不结束开发或标记完成。未操作 8860/8880、用户作品/数据库/网关/凭据、测试机，未 push/部署/安装 OCI。

### G4.14 用户/工作区 MCP 凭据（2026-09-07）

- schema 49 新增 AES-256-GCM 加密连接凭据和审批快照中的执行用户绑定。凭据字段由受信配置的 credential_headers / credential_environment 开放，用户只能填写值，不能设置地址、命令、目标环境变量或任意 env:// 引用。个人优先于工作区；个人撤销可回退共享凭据，确认框明确告知。缺密钥不降级明文，仍允许撤销。未修改实际运行环境或配置真实密钥。
- 管理 API 复查当前成员和角色、CAS 与同请求重试；个人为 editor 以上，工作区为 admin/owner。凭据不回显，不进入通用命令正文哈希/账本。内部解析必须具备有效 service + 精确执行身份，在事务内复查租户、作者、状态/租约、角色及连接绑定；仅交给原生 SDK 的连接局部参数，不修改进程环境或写入模型上下文/checkpoint。
- 变更/撤销会改变工具配置指纹，旧连接不能用旧审批启动新调用；已发出的外部请求不能撤回。删除工作区清除当前密文；不宣称历史备份字节或提供方 token 被删除。缺凭据的 MCP 不向普通 Agent 提供，所选依赖仍失败关闭。旧引用 inventory 保持惰性，未授权读取宿主 env/vault。
- 原生 SDK Python 专项最终 61 项通过，36.55s，`.tmp/goal-g414-credentials-python3.xml`：stdio 三执行上下文、HTTP/SSE 各两个用户连接、实际认证头、生产 RunState 序列化无凭据、错误回执/控制字符/字段大小及 MCP 输出回归。此前 60 通过/1 失败是新测试错误假定 HTTP 请求次数，修正为逐用户实际请求与认证结果检查；生产传输未放宽。
- Go 最后 Runtime / HTTP / agenttool 专项通过，26.465s / 6.779s / 1.229s，`.tmp/goal-g414-connections-go-final.log`。覆盖密文、同请求与版本竞争、重开、个人/工作区/跨租户选择、权限降级、伪造身份、终态、旧审批、撤销 tombstone、工作区删除、48 -> 49 备份迁移和旧引用不激活。HTTP 初次新断言把无服务凭据应得的 401 写成 403，修正后通过；生产认证未改。
- 真实 Go HTTP + 固定 SDK Runner + stdio MCP 三执行身份联合通过，40.189s，`.tmp/goal-g414-credentials-sdk-http2.log`。模拟密钥/凭据进入临时加密数据库，经真实内部解析交给 MCP；主对话/后台/状态化均校验凭据并输出文件，重开后检查审计/资产下载，生产检查点不含凭据。初次脚本绕过生产上下文序列化导致 Task deepcopy 失败，改用实际 `_serialize_paused_run` 后通过，不修改 SDK。
- 前端增加 Skill 管理 > MCP > 连接凭据，提供个人/工作区范围、遮掩输入、保存/更新/撤销、权限/冲突和未配置密钥状态。最新组件测试 11 项与类型检查通过，`.tmp/goal-g414-connections-frontend-final.log`。桌面/手机隔离 API fixture 已通过并查看截图；最终范围保持的小修后再次验收，日志 `.tmp/goal-g414-connections-ui-final.log`，报告 `.tmp/goal-g414-connections-ui/report.json`。UI 测试不调用模型或真实账户。
- 实施与测试边界见 `agent-tool-plane.md`：部署时必须由操作方提供独立 32 字节加密密钥；本批未替用户配置密钥、账户、网关或服务。没有 OAuth 授权代理、密钥轮换/历史备份擦除承诺；已有服务环境引用仍按原受信配置使用。不是全模式真实外部账户/模型验收，也不是整体功能达标或最终 review。

### G4.15 规则管理与对话作者接线（2026-09-07）

- schema 50 新增三范围不可变规则版本、每次执行的固定引用和私有规则变更提案。个人仍限定当前工作区本人，工作区编辑须 admin/owner，作品编辑须当前工作区 editor 以上；真实权限在事务中复查。规则版本及提案纳入存储配额、作品删除影响和工作区删除，不声称清空能擦除历史执行或备份。
- SDK 接线使用固定版本的 Agent.instructions，不替换 Session/compaction，也不把依赖 Sandbox Shell 的原生 Memory 当作本功能。三种实际执行入口在模型调用前加载后端固定快照，主执行、后台、状态化及修复使用其 instructions。检查点只新增快照哈希，不额外复制规则正文；升级前未有快照的旧 checkpoint/结果拒收修复不套用后来新增规则。
- 页面已增加“Agent 规则”全局入口和作品入口，个人/工作区/作品范围、全文编辑、启停、清空、版本冲突保留草稿、只读和离开未保存提示。组件 6 项、tsc 通过；1440x900 / 390x844 隔离 API fixture 保存/刷新/冲突/权限/清空和布局通过，截图已查看。日志 `.tmp/goal-g415-instructions-ui.log` 与报告 `.tmp/goal-g415-instructions-ui/report.json`。这是 UI fixture，不是外部模型 E2E。
- Python 127 项通过，35.12s，`.tmp/goal-g415-sdk-regression2.xml`：包含实际主 Runner、后台 Worker、状态化 Worker 的三范围模型 instructions 断言，以及固定 SDK/暂停基础回归。第一次默认权限运行因 pytest 临时目录 WinError 5 未完整通过，保留失败日志；随后经工具审查使用隔离目录运行，未更改 ACL/系统策略。
- Runtime 最初规则专项通过 20.733s；新增规则工具作者隔离、原生审批前禁止写入、写入后同调用幂等、旧规则版本冲突、撤权及共享结果/错误正文脱敏专项通过 34.497s，`.tmp/goal-g415-rule-tools-go.log`。HTTP 基础通过 5.053s，`.tmp/goal-g415-instructions-http2.log`；初次新断言应为 service 写用户接口被既有中间件拒绝 403 而非 400，已修正测试，未放宽权限。
- 对话 get_saved_instructions / update_saved_instructions、作者专用提案预览、规则确认卡已接代码，确认卡与管理页最新组件合计 11 项通过，`.tmp/goal-g415-author-frontend.log`。原生 SDK 作者审批恢复、新内部工具 HTTP、同场后端与 SDK 联跑及进一步 review 尚在进行，GAP-17 未标完成。

#### G4.15 后续复核

- 对话作者原生 SDK 的批准/拒绝三身份专项 7 项通过；真实 Go HTTP + 固定 SDK + 确定性模型三身份联跑通过，首次 143.762s，新增持久写入回执门禁后最终 80.007s，`.tmp/goal-g415-author-receipt-sdk-http.log`。涉及原生暂停、RunState 序列化重建、本人批准、冻结旧规则不漂移和后端重开后的新规则；这不是外部模型加浏览器同场 E2E。
- review 发现工具校验错误可能以普通 SDK tool output 返回，已要求 Go 查到对应不可变规则写入回执才可完成写工具，避免确认卡虚报已保存。缺少指令快照时也在写入前拒绝；失败提示不再断言一定没有保存。迁移/删除/权限/冻结专项通过；最终写回执相关 Runtime 55.890s、HTTP 9.131s，`.tmp/goal-g415-write-receipt-go.log`。
- 真实 App/mock API 的 1440/390 规则确认、正文清除、既有暂停/追加交互复验通过，`.tmp/goal-g415-rule-approval-ui-final.log`。截图发现手机首页标题和导航被压成窄列，已修复并增加宽度/重叠断言；最终截图重新查看。规则编辑页此前分层证据保留。
- 本版 Sidecar 全量为 **723 通过、3 失败，426.41s**，`.tmp/goal-g415-sdk-all.xml`。失败集中在 stdio MCP 初始化/动态加载，尚不能宣称全量稳定；没有调整生产超时、系统策略或网关。继续串行定位及最终同版核定。GAP-17 仍待最终联合验收，不用局部通过宣布 Goal 完成。

### G4.16 旧执行追加历史入口（分层验收通过）

- 增加同作品/Run 范围的历史尝试摘要分页，每页 50，仅返回标识、尝试次数和收件计数，不投影私有 checkpoint/令牌/输入正文。用户按需打开历史记录，再用已有受权接口读取旧 attempt 的正文及 received/included 状态；重试或刷新不丢入口，未提交草稿禁止换历史目标。
- Runtime 53 条跨页/重建/旧记录/隔离专项 14.525s，HTTP 登录/viewer/跨工作区专项 14.307s，`.tmp/goal-g416-input-history-go-http.log`。类型检查通过；完整前端回归与浏览器验证仍在进行，未更新用户数据库或服务。
- 后续完整前端 319 项、0 失败/错误，46.709s，`.tmp/goal-g416-frontend-all.xml`；真实 App/mock API 1440/390 的历史列表、刷新后选择旧尝试、只读收件与长标识布局通过，截图已查看，`.tmp/goal-g416-history-ui.log`。
- G4.15 全量 3 项 MCP 失败串行复验为 4 通过/1 失败。查明作者 fixture 从简单工具继承 `load_skill_instructions=5s`，实际 Go 注册值 30s、安装工具 120s；单次本机 MCP 模块导入计时 4.575s。将相关测试目录对齐正式工具时限，生产参数未改变。随后 **Sidecar 全量 726 项通过、0 失败/错误/跳过，257.89s**，`.tmp/goal-g416-sdk-all.xml`；这不是原生 Sandbox/外部工具或真实模型验收。

### G4.17 追加要求撤回与修订（分层联合验收通过）

- schema 51 给三种追加记录增加撤回时间、替代记录引用；原正文、原序号和既有收件保持不变。变更必须是本人、当前有效 editor 权限、尚未领取且未消费/未变更的记录。已领取即使 UI 尚未显示 included，也拒绝撤回/覆盖，防止与执行器竞争；修订新增记录，退出或取消后的记录只能撤回未领取项，不能创建新执行要求。
- SDK 仍用原生 add_input。Go 领取、状态化心跳及 checkpoint 校验排除撤回/被替代记录；固定 SDK 三模式接受不可变序号的合法间隔，同时拒绝重复、逆序、bool、零及越界。原 ID/内容哈希和已领取 checkpoint 前缀仍严格校验，不修改已冻结输入或偷偷回滚副作用。
- 前端通用追加组件已接本人可用的修订/撤回图标、确认、失败保留草稿/幂等键、目标锁定、领取/撤权后禁用、不可混淆的历史状态和 SSE 更新。组件 28 项通过；实际 App/mock API 1440/390 的修订、撤回、再追加、刷新及旧尝试历史通过，`.tmp/goal-g417-input-change-ui.log`，截图已查看。前端完整回归正在运行。
- Runtime 三模式及原追加回归 83.762s 通过，`.tmp/goal-g417-input-change-go-second.log`；新 schema 50 -> 51 备份迁移及固定 checkpoint、三模式边界最终 28.100s 通过（联合命令中的 Runtime 包）。初次新测试写了不存在的 ServiceID 字段，修正为现有 ServicePrincipal helper，未改正式身份契约。
- Sidecar 首轮 68 通过/3 新 fixture 失败，原因是流式模型误走非流式 Runner；修正为原生 run_streamed 后 71 项通过，70.37s，`.tmp/goal-g417-sdk-input-final.xml`。Go+SDK 联跑首轮被 Windows Access denied 阻止启动 HTTP 测试二进制，保留 `.tmp/goal-g417-input-changes-sdk-http.log`；后续含迁移的新批沿用相同缓存/临时目录和系统策略正在复验，不能提前标记联合通过。

#### G4.17 后续复核

- 最终 Runtime 22.964s、HTTP + 三模式实际 SDK 重建 103.881s 通过，`.tmp/goal-g417-changes-final-joint.log`。新增真实领取/撤回竞争、双修订单赢家、终态只能撤回、正文限制、原记录不变专项；HTTP 包含本人、viewer、跨作品/工作区、服务身份、严格请求体、幂等重建。三模式均只把新修订送入原生 SDK，未重放原工具，原文件版本和累计用量保持正确。
- 联跑前轮保留失败：后台新修订 fixture 错误断言公开历史仅一条，现改为显式 withdrawn/superseded/included 三条、替代 ID 和模型不含已移除正文；主对话启动超时独立复现，模块导入实际 21.3005s 超过原 20s。仅测试启动门限改为 45s，原生产参数、执行和停止期限未改，最终通过。
- 完整前端 **323 项、0 失败/错误，68.207s**，`.tmp/goal-g417-frontend-all.xml`。与 SDK 71 项专项和桌面/手机 mock API 分层证据分开；没有真实外部模型或用户环境验收声明，整个 Goal 仍未完成。

### G4.18 当前执行追加附件与媒体快照校验（2026-09-07，复核中）

- schema 52 给三模式输入保存权威附件快照：仅接受当前作品的精确 asset/snapshot，单条最多 4 个、单文件 5 MiB、执行累计 8 MiB，撤回/修订保留历史并计入限额。SDK 原生输入携带图片及有界文本材料，视频/归档只声明精确引用而不虚称已读取内容；旧纯文本哈希和 SDK Session/compaction 不变。管理和执行仍由原平台授权、租约及不可变账本保护。
- 前端三模式追加复用同一材料选择/上传组件，支持仅附件提交、部分上传成功保留、丢失响应重用幂等键、草稿锁定执行目标、权限变化禁用，以及历史附件名称/快照。API 只接收引用，不相信客户端文件名/大小。专项组件 5 项通过，桌面/手机真实 App + mock API 验证通过，`.tmp/goal-g418-input-attachment-ui/report.json`；这不是外部模型 E2E。
- review 修复图片/视频下载检查元数据后未把快照带入 content 请求、未复查字节的问题。现在精确快照查询和大小/SHA-256 校验与 Hosted 材料统一；HTTP 字节竞争、过期、身份、MIME 和内容变化专项合计 133 项通过，51.51s，`.tmp/goal-g418-media-reviewed.xml`。
- 三模式原生输入与旧追加兼容专项 97 项通过，43.02s；本批较早完整 Sidecar 800 项通过，362.44s，`.tmp/goal-g418-sdk-all.xml`。随后大图片零模型暂停复现 Worker 检查点重复存储超限：4 MiB 图片同时存在 SDK original_input 和 Worker 副本。改为大输入只在 Worker 保存摘要并校验原生 RunState 原输入，保留原大小上限，不作会话截断或本地摘要。最终 91 项通过，50.68s，`.tmp/goal-g418-large-image-final.xml`；篡改必须在模型调用前以已有 SDK_EXECUTION_STATE_INVALID 拒绝。
- 首次 Go+SDK 附件联跑被 Windows 拒绝启动测试二进制；经审查、相同目录和命令重新运行，三模式均通过，263.665s，`.tmp/goal-g418-attachments-sdk-http-reviewed.log`。真实公共上传 API 建立文本和 PNG，追加后修订、关闭/重建 Go 和 SDK，核对原生图片字节、文本正文、只消费修订、原文件版本仍 1、用量和历史。模型为确定性 fixture，未访问 8860/8880 或用户数据库。
- 后续 review 又确认重新解析能改变相同文件快照下的文本，已追加固定 text_hash 并在 SDK 读取时校验，修订不接受已变化的解析版本；同时接原始 blob 的归属/可用状态/大小/校验和检查。新增 51 -> 52 备份迁移、重建领取、累计大小、幂等重试、坏解析/原文件记录与文本篡改测试，最终 Go/SDK 回归正在运行。上面的 800 项和联合测试早于这一修正，不冒充最终同版通过。
- 前端全量进程长时间没有终态，已中断本次测试并启动有逐文件输出的诊断回归，不把中断算作成功，不操作无法确认归属的宿主进程。整个 Goal 和最终全局 review 仍未完成，接下来按原基线继续失败安全恢复、原生工作区与通用协作。

#### G4.18 后续核定

- 解析文本哈希、原始 blob 检查以及 51 -> 52 迁移/领取/限额/幂等专项通过，Runtime 61.985s，`.tmp/goal-g418-attachment-boundaries.log`。最终完整前端 **328 项通过，672.42s**，`.tmp/goal-g418-frontend-diagnostic.xml`；环境初始化 377.09s、实际断言 100.18s，前一轮被中断的报告不算成功。1440 桌面附件截图也已检查。
- 本版完整 Sidecar 为 **803 通过、10 失败，710.50s**，`.tmp/goal-g418-sdk-final.xml`。失败都涉及 stdio MCP 10s 连接启动或重连及后续工具不可用。前端结束后串行相关专项为 80 通过、4 失败，251.90s，`.tmp/goal-g419-native-mcp-serial.xml`；保留失败，不凭前版 800 或局部成功宣布同版全绿。三模式实际 Go+SDK 附件最终复跑另行进行，未改生产 MCP 超时或系统策略。

### G4.19 失败收件事实与原生恢复边界（2026-09-07，进行中）

- review 复现主对话/后台在追加已送入模型、输出 guardrail 随后拒绝时漏记消费事实；之前只有状态化入口在 finally 保存回执。`.tmp/goal-g419-receipts-before.xml` 为 2 通过/2 失败。已将主对话回执作为终态前的受信内部流事件，Go 消费后发公开状态更新；后台失败提交携带已消费 ID 并与失败状态同事务写入。背景回执还统一核对当前领取的有序前缀，拒绝迟到、跳项、逆序和重复，不仅核对 ID 归属。
- 同时修复后台输出 guardrail 被归为可重试提供方故障的问题。SDK 专项 **86 项通过，71.75s**，`.tmp/goal-g419-receipts-fixed.xml`，覆盖输入拒绝不消费、输出拒绝保留真实收件、原追加/附件/Backend 和原生失败恢复基础。Go 权限/事务/持久事件以及三模式附件联合正在验证，不提前宣称完成。
- 固定 SDK 原生失败恢复试验 2 项通过（在上述串行 MCP 批次和 86 项中均实际运行）：首次模型调用失败、已完成写工具后下一模型调用失败，`RunResultStreaming.to_state` 经 JSON 序列化和 `RunState.from_json` 可继续，不重放已完成工具、不重置最大 turns、保留既有用量。该结论仅证明 SDK 原生能力，不等于三模式生产失败继续入口已开发。
- 下一步按真实模型调用边界保存可恢复的提供方失败 checkpoint，只允许确认无未决副作用的原执行人工继续；输出安全拒绝、权限/快照损坏、取消或外部写入结果不确定不走自动重跑。还需保留失败原因、受权核对/回执、原生恢复与 UI 操作，再推进原工作区/协作及最终全量验收和全局 review。目标不缩小，也不以本段阶段性记录结束。

#### G4.19 原生模型故障恢复接线与联合验证（2026-09-08 核定）

- 三模式生产 Runner 已通过固定 SDK RunHooks 区分模型暂时失败和工具失败。仅连接故障、408/429/5xx、仍有原轮次预算且无未决工具的模型边界可以保存原生 RunState；Go 再用原执行的工具账本拒绝未决审批、运行中工具及已经发出但结果未确认的写入。必须显式继续，保留原 attempt、轮次、上下文和文件版本，不新建执行。公开故障原因在恢复后清除，前端沿用原继续命令。
- review 发现新增主对话输入收件事件漏加 Sidecar Pydantic 白名单，实际 HTTP 事件流被 ValidationError 截断，已修复契约并加测试。先前 `.tmp/goal-g419-attachments-joint-final.log` 主对话失败、后台/状态化通过，不算整批通过；下方为修复后联合重跑。
- 模型请求失败时，SDK original_input 或原生 InputItem 已保存输入，但不等于模型返回了收件证据。恢复逐项核验原生精确输入，不重复追加或伪报收件。首次模型响应前的 RunState 不允许 add_input：通过原生模型恢复达到可接收边界后再追加；原执行若已终止则保留未确认回执。首轮恢复直接 after_turn 取消导致无进展递归也已复现并修复，改为 on_llm_start 后取消并增加无进展检查；没有修改 SDK 内部状态或伪造历史。
- 原生多次故障/追加、原暂停/审批、收件/Backend 专项 **146 项通过，748.60s**，`.tmp/goal-g419-recovery-full-focused.xml`，不是完整 Sidecar 回归。首次恢复 6 项曾 3 失败；修正后多次故障场景继续暴露无进展问题，最终专项包含全部修复分支。一次测试模型误调用当前 Agent 未提供的工具，也保留失败报告并改用本场景已有的 load_skill_instructions。
- Go 回执事务/持久事件专项 Runtime 15.164s、HTTP 27.626s 通过，`.tmp/goal-g419-receipts-go-final.log`；三模式恢复门禁、原执行重建、幂等及旧令牌拒绝专项 Runtime 11.390s 通过，`.tmp/goal-g419-recovery-go-fixed.log`。较早宽范围 Runtime 批次在 240s 总限超时，不能算全通过。
- **三模式模型故障恢复 + 三模式附件 Go/SDK HTTP 联合通过，140.237s**，`.tmp/goal-g419-native-recovery-joint.log`。每模式使用临时数据库，关闭并重建 Go/SDK，工具保存文件后故障，恢复后版本仍 1、原执行未替换、用量连续；状态化还覆盖故障时追加要求及人工恢复后仅消费一次。确定性模拟模型，不是外部模型 E2E；未访问 8860/8880、用户数据库或测试机。
- TypeScript 检查通过；前端恢复/暂停/追加/流程状态专项 **68 项通过，31.10s**，`.tmp/goal-g419-ui-focused.xml`。新浏览器截图脚本默认启动被 EPERM 拒绝，两次自动权限审查均超时，未完成截图复验，已向用户说明。未变更系统安全策略；diff 检查退出 0。完整 SDK/最终同版全量、未决外部写入核对、原生持久工作区、通用协作与最终全局 review 继续执行，不在本阶段结束。

### 当前动作

- G4.19 追加复核发现非流式兼容入口在拒绝保存恢复 checkpoint 时仍保留失败轮的 SDK Session。真实 SQLite 初次模型失败/读取工具后失败两例先失败（`.tmp/goal-g419-nonstream-before.xml`），改为在 Session 回滚边界内拒绝不支持的检查点传输后，相关 **123 项通过，42.92s**（`.tmp/goal-g419-nonstream-fixed.xml`）；正式流式暂停仍保留原状态。此修复在下方 841 项全量之后，不能混算为同版证据。
- G4.19 Go 首次全包失败包含旧目录工具数量 29/实际 31 的断言及 Runtime 整包 600s 上限；超时中的子测试仅运行 2s，不据此认定死锁。数量断言及新增两种规则工具的权限/审批断言已补，按包串行并输出逐项 JSON 的全包复验退出 0、无失败（`.tmp/goal-g419-go-all-diagnostic.jsonl`），Runtime **883.408s**、HTTP **104.604s**。只增加测试整包时限，未改生产超时。
- G4.19 前端全量首次 **330 通过/1 失败**（`.tmp/goal-g419-ui-all.xml`），发现凭据编辑清理 effect 可能关闭新打开的表单。改为编辑内容绑定配置版本/范围/字段/权限，清理 effect 只丢弃过期绑定；新增版本、字段、权限、存储变化的清空测试，专项 10 项通过。TypeScript 通过；最终完整前端 **335 项通过，181.67s**（`.tmp/goal-g419-ui-rechecked.xml`）。未启动浏览器，之前截图权限待确认边界不变。

#### G4.20 通用子任务接线、持久结果与操作入口（2026-09-08，回归中）

- 新增 `subagents.py`，使用 SDK `Agent.as_tool`，不另写模型任务调度循环。独立子任务可读取限定的作品/文件/历史工具并生成分析或草稿；不提供写入、安装、外部工具、生命周期控制或递归委派。继承同一执行身份及冻结规则，复制独立应用上下文、限定输入输出和原生轮次预算，工具调用按父调用命名空间隔离，避免两个子任务使用同一个模型调用 ID 时串回执。
- 验证三执行身份下两个子任务真实并行读取、汇总、累计 SDK 用量、父执行原生暂停/JSON 重建后不重复调用；验证父取消会终止两个读取并记录取消。SDK 立即取消后仍有已取消的批处理清理任务，测试须等其自然结束；未修改 SDK、忽略未取消任务或取消其他执行任务。加入本执行 `SubtaskScope` 等待已登记的子任务处理收尾。
- 主对话、后台和状态化的共享 AgentToolProvider 已接构建入口，仅当可信 Go 目录显式登记 `runtime:delegate_subtask` 为 read/never 且提供平台 guardrail 时开放。源码目录已登记；每原执行最多 8 个子任务，每子任务最多 64 次可信读取，数据库事务内核对归属并计数，幂等回执不重复计数。没有重启用户环境。基础原生与目录专项 **25 项通过，4.80s**（`.tmp/goal-g420-provider-subtasks.xml`）；基础版完整 SDK **852 项通过，157.62s**（`.tmp/goal-g420-sdk-all.xml`），此证据在以下持久结果改动之前。
- 源码 schema 53 增加子任务读取归属、确切产物版本与完整结果表。结果只回传结论和版本引用，不重复夹带已读取的全部产物正文；Go 拒绝其他子任务的读取、未完成读取、未读取版本和跨作品版本。完整结果与调用完成同事务写入，计入存储配额，校验哈希、支持重建后读取；共享审计只留标题/结果可用标记。Sidecar 子任务必须取得完成回执，保存异常不能静默当成功。
- 现有 Agent 执行记录增加子任务标题/状态、父子读取分组及完整结果展开。仅成功任务开放结果读取，跨执行迟到响应丢弃，请求失败可重试读取但不重新执行；结果按纯文本呈现。作品删除预览纳入子任务正文数量与结果哈希，作品/工作区删除清理正文，保留审计关联。API 复用用户/租户与已删除项目门禁，响应 no-store。
- Go 结果、配额、只读/父归属、重建、迁移备份与删除专项通过 **29.091s**（`.tmp/goal-g420-subtask-store-regression.log`，早于最后追加的启动门禁/预览数量断言）；前端专项 **9 项通过，16.98s**（`.tmp/goal-g420-subtask-ui.xml`），TypeScript 通过。SDK 子任务专项 **18 项通过，8.91s**（`.tmp/goal-g420-subtask-bounded-fixed.xml`）。首次长测试参数被 pytest 当作测试名，触发 Windows 环境变量长度限制，修正为短测试 ID；保留失败报告，没有放宽业务长度限制。
- **真实三个生产入口 + Go HTTP 联合通过，28.399s**（`.tmp/goal-g420-subtasks-joint-verified.log`）：OpenAIAgentsRuntime、SDKBackgroundTaskWorker、SDKTaskWorker 分别进行两路 SDK 原生并行分析、同模型读取 ID 隔离、真实版本读取、汇总与正式提交，Go 重建后完整结果仍可读，viewer 可读本工作区、外部租户拒绝。状态化确认原 attempt succeeded 与累计 SDK 6 次请求。均为确定性模拟模型，不是外部模型或浏览器 E2E。初次执行被 Application Control 拦截；同命令经审查后可运行，未改策略/改名换路径绕过。前两次业务夹具分别缺幂等元数据、模拟模型名不符合原生 compaction 模型前缀；修正夹具后全三模式重跑通过。
- 本版完整 SDK **860 项通过，279.05s**（`.tmp/goal-g420-sdk-durable-all.xml`），完整前端 **340 项通过，192.63s**（`.tmp/goal-g420-ui-all.xml`），TypeScript 通过。Go 全量退出 0、无失败，Runtime **849.105s**、HTTP **151.210s**（`.tmp/goal-g420-go-all.jsonl`）；浏览器启动权限仍待用户答复，未重试截图。父原生 checkpoint 后已完成子任务不重放/取消收尾已有 SDK 层证据，尚需跨进程暂停/故障分支联合验收；通用协作整项仍未勾选。原生工作区/二进制/Shell、未决外部写入核对和最终全局 review 继续推进。

#### G4.21 外部工具结果核对恢复前置试验（尚未接生产）

- 固定 SDK 实验复现：一批并行写入中有工具抛出未处理错误，流结束后直接 `to_state` 的 `current_step=null`、`generated_items=[]`，恢复不包含该批已发出操作。`.tmp/goal-g421-tool-recovery-probe.log` 的双回执断言失败，因此不能直接套用模型故障 checkpoint 或声称外部操作可原样继续。
- 第二试验将不确定结果作为明确 `outcome_unknown` 的原生工具输出，在 `RunHooks.on_tool_end` 请求 `cancel(mode="after_turn")`。SDK 原生 state 完整保存该批调用/输出，JSON 重建后用原生 `add_input` 追加显式用户核对事实，原操作不重复、原 6 turns 预算保持、总请求 2。`.tmp/goal-g421-tool-review-pause-probe-fixed.log` 退出 0；未修改 SDK 私有状态或编造提供方成功回执。首版试验错误地给 `add_input` 传单个 dict（应为 input item 列表），修正试验后通过。
- 这只是隔离可行性验证，不是已开放功能。下一步仍需接 Go 不可变核对记录、执行/发起人/参数绑定与继续门禁，三入口的原生暂停传输、用户核对输入、前端操作和并发提交阻断；完全失联而未取得原生 checkpoint 的执行不得伪装可恢复。不覆盖真实外部服务的 exactly-once 保证，也不能解除未验证的写入重放拦截。

#### G4.21 提交检查与人工核对记录（2026-09-08，恢复接线中）

- 真实 Store 复现主对话、后台和状态化都允许已发出的 MCP 写入失败后提交成功，并允许主对话提交开始后登记新写入，`.tmp/goal-g421-terminal-before.log` 四个分支失败。现于主对话开始/完成提交、后台完成、状态化收件/正式提交的事务内阻断未决外部写入，并在主对话工具登记/启动事务检查执行仍可运行。保留原审批错误优先级。暂停/审批/模型恢复及竞争、只读失败、未启动取消、旧结果再次提交专项通过 **120.125s**（`.tmp/goal-g421-terminal-reviewed.log`）；这是后续改动，之前 G4.20 全量不是本版全量。
- schema 54 新增不可覆盖的人工核对记录，绑定原调用、原执行/发起人、参数及配置快照。原用户可记录 `applied` 或 `not_applied` 及实际检查依据；不能由 Agent/服务或其他成员代确认，仍保留原失败状态，不伪造提供方成功回执，也不自动重跑。记录有内容哈希、幂等请求、存储配额、删除预览哈希/数量与正文清理。三模式权限/不可变回执、53 -> 54 备份迁移、重建/配额/删除专项通过 **15.058s**（`.tmp/goal-g421-reconciliation-store.log`）。
- `GET/POST .../agent-tool-calls/{id}/outcome-review` 与执行历史核对入口已接，按需读取、由用户明确选择并填写依据、丢失回执重用请求 ID、跨调用迟到响应丢弃、纯文本显示。HTTP 原发起人/viewer/其他编辑者/跨租户/服务、严格请求体与幂等核对通过 **6.822s**（`.tmp/goal-g421-reconciliation-http.log`），组件及执行记录 **11 项通过**（`.tmp/goal-g421-outcome-ui.xml`）。没有启动浏览器或操作用户环境。
- 应用配置 `tsc -p tsconfig.app.json --noEmit` 初次发现两个旧类型问题：AgentToolCall 缺少后端实际返回的 arguments_hash，追加回执 Record 索引被静态推断为必定存在。已补字段并用 Partial 表示可能没有本地回执，本命令退出 0（`.tmp/goal-g421-outcome-ui-types-fixed.log`）。此前仅有空日志的类型检查不能证明遍历了应用配置，后续固定用实际应用配置，不能以根配置空 files 的成功代替。
- 默认 vmThreads 全量为 **336 通过/10 失败**（`.tmp/goal-g421-outcome-ui-all.xml`），失败集中于两个按需读取组件的 API mock 与组件引用不一致；同样用例在独立 threads 配置下 **346 项全通过**（`.tmp/goal-g421-outcome-ui-all-threads.xml`），没有改断言或生产组件来掩盖失败。默认 test 命令改为独立 threads，与 G4.20 已通过的完整回归设置一致。保留失败报告，不冒充浏览器或外部模型 E2E。
- `tool_outcomes.py` 已定义明确的 MCP 未确认结果标记，固定 SDK 的真实 MCPServer 工具接口保存 `raw_item.output=[{type:input_text,text:...}]`，并行两项操作只发生一次，原生 after_turn 暂停、JSON 重建及用户事实 add_input 后用量/轮次保持。**6 项通过，10.65s**（`.tmp/goal-g421-native-mcp-outcomes.xml/log`），接口实现为隔离模拟服务，尚未连接生产 Audited MCP 和三生产 Runner。
- **尚未完成**：不能凭已保存人工记录放行执行。下一步把核对事实纳入原追加输入/原生 SDK 恢复链路，绑定不可编辑的输入记录并校验 checkpoint 中每个原 MCP 调用及未确认输出，防止遗漏并行写入、伪造回执、已失联无 checkpoint 的重跑。再接三模式暂停原因/显式继续门禁、实际 Go+SDK 重建联合，并收口原生工作区及原范围剩余验收，最后全局 review。Goal 继续，未达标不勾选。

- G4.19 后续同版核定：首次完整 SDK 为 `832 passed / 5 failed`，`.tmp/goal-g419-sdk-full.xml`。其中四项旧测试只拦截 `Runner.run`，执行改为原生流式后已未覆盖目标路径；现改用真实 SDK Runner/SQLiteSession 和确定性模型验证提供方失败、终态提交修复、目录按需加载及未提交回滚，另修正非流式兼容入口把模型恢复误报为审批。三模式新增首次模型恢复直接结束时迟到输入不虚报收件的验证。专项 **85 项通过，30.98s**，`.tmp/goal-g419-provider-terminal.xml`；最终完整 SDK **841 项通过，199.73s**，`.tmp/goal-g419-sdk-rechecked.xml`。MCP 并发激活的单次 10s 连接超时在专项和全量复测均未复现，未修改生产或测试连接超时；保留初次失败证据，不将原因断言为环境。默认沙箱临时目录清理拒绝的运行记为失败，正式通过来自经审查的同一路径测试。Go/前端扩大回归和其余原始缺口继续推进。

- G4.13-G4.17 的文件、凭据、规则、旧执行历史及输入撤回/修订已有接线和分层联合证据，不再重复实现。当前收口 G4.18 附件与快照/解析版本校验，继续失败安全核对和其他原范围，不把分层测试当作最终真实模型验收。
- 按原能力基线继续原生工作区/Shell/二进制、通用子任务协作；这些仍含未完成后端代码。此前作者 MCP 失败本轮串行复测通过，仍保留旧失败与最后全量核定要求，不绕过 Application Control。
- 继续副作用失败后的安全核对与继续；旧 attempt 历史入口、三种文本追加、撤回/修订、正式提交拒收修复、首次领取队列饥饿和现有 inline Skill 按需发现已有分项证据，不重复实现。
- 完成上述原范围后，重新执行同版全量回归与真实模型/Go/前端三模式联合验收，再做最终 review。当前 review 不通过，不能作为原目标完成或部署的依据。详见 `agent-platform-global-review-20260907.md`。

#### G4.21 三入口生产恢复接线与跨进程核对（2026-09-08，扩大回归中）

- schema 55 增加核对事实与原追加输入的不可变关联。显式继续在同一事务核验原用户、原执行、原调用参数及 SDK 原生调用/输出，逐项生成受保护的用户事实输入；不修改 SDK RunState、不把人工事实伪造成提供方成功。重复继续不重复追加，普通输入撤回/修订不能修改核对事实，计入配额与删除清理。54 -> 55 备份迁移、重建、配额失败与三模式隔离通过，`.tmp/goal-g421-native-fact-migration.log`，86.030s。
- Audited MCP 与三个生产 Runner 已接原生 `after_turn` 暂停和明确 `external_tool_outcome_unknown` 原因。主对话、后台、状态化均在原执行恢复；必须人工核对后明确继续，未处理并行调用、无有效 checkpoint、审计写入失败均不允许伪恢复。Go 扩大恢复/模型故障/输入修改门禁通过，`.tmp/goal-g421-recovery-gates-final.log`，55.048s；已消费事实可跨后续领取继续，未消费事实仍必须绑定当前领取。
- **实际 Go HTTP + 三个生产 SDK 入口的审批、发出、暂停、关闭并重建、人工核对、原执行继续整批通过**，`.tmp/goal-g421-outcomes-joint-reviewed.log`，64.513s。每模式两个已审批操作，一个提供方回执丢失，最后仍仅发出两次；原失败调用不改写成功，核对输入仅消费一次，后台/状态化不新建 attempt，用量连续。主对话使用实际事件序列及 manager 持久化方法，不冒充完整 HTTP SSE/browser 验收。模型与 MCP 传输为确定性夹具，不连接真实外部服务。
- 联合验证先后保留了夹具非法/重复幂等键、空 JSON 请求体、错误成功状态码的失败报告。另一轮主对话/状态化通过但后台子进程启动被拒绝，`.tmp/goal-g421-outcomes-joint-verified.log`，不算整批通过；同路径经审查后取得上述全三模式结果，未更改系统策略。
- 新负向测试发现 SDK 默认错误处理会把审计失联转成可继续的工具输出。现使用原生 MCP `failure_error_function` 对审计/调用绑定失效中止执行，只有可靠保存的未知结果可暂停核对。修正后专项 **56 项通过，13.38s**，`.tmp/goal-g421-sdk-audit-failclosed.xml`；后续还在核定异常分类，不能将其误报为可自动重试的模型服务故障。
- 前端明确区分等待核对外部操作与等待恢复模型连接，继续失败保留真实 API 提示；专项54项通过，完整前端 **348 项通过，31.986s**，`.tmp/goal-g421-ui-all-final.xml`。类型检查针对实际应用配置通过。旧69项专项1失败是夹具用普通 Error 代替 ApiError，按实际契约修正；默认 bundle 配置加载失败的测试不算通过，正式回归使用仓库既有 native 配置加载。浏览器权限边界不变。
- 首轮完整 SDK **883 通过/6 失败，186.19s**，`.tmp/goal-g421-sdk-all.xml`。旧作者/MCP 恢复夹具缺少 Go 真实响应中的参数哈希，已补真实契约；异常分类与最终 SDK 全量正在复验。Go 全包也在运行，本段不是最终同版全量结论。
- 下一步仍是原范围的原生工作区/Shell/二进制接线、子任务跨进程暂停/故障联合，以及最终完整验收和全局 review。GAP-08 不能因 OCI 不可用而把缺失适配代码算完成，GAP-16 的单次成功联跑不替代恢复验收；不 push、不部署、不操作用户作品或 8860/8880。

#### G4.21 最终回归与 G4.22 子任务恢复验收（2026-09-08）

- G4.21 异常分类专项 **23 项通过，12.82s**，`.tmp/goal-g421-sdk-typed-error.xml`。SDK 会用原生 UserError 包装审计异常，检查原因链并验证两个 Worker 返回不可自动重试的工具配置错误；保留错误外层类型断言不符的整批失败报告 `.tmp/goal-g421-sdk-all-rechecked.xml`（883 通过/6 失败），不修改 SDK 包装机制。
- 最终完整 SDK **889 项通过，232.75s**，`.tmp/goal-g421-sdk-all-final.xml`；完整前端348项及实际应用配置类型检查通过，`.tmp/goal-g421-app-types-final.log`。Go 全包退出0、无失败，`.tmp/goal-g421-go-all.jsonl`，Runtime1297.62s、HTTP112.84s。仅核定本批已接代码，不等于全部能力或最终 review 达标。
- G4.22 增加真实三生产 SDK 入口的子任务故障后跨进程恢复：先完成两个只读子任务及其原版本读取，父模型请求失败，保存原生状态；关闭并重建 Go/SDK 后显式继续，只发生一次最终汇总，两个子任务均不重放，结果/版本关联和累计用量保留。与原成功路径和最终外部核对恢复一起，联合整批通过 **223.052s**，`.tmp/goal-g422-subtask-outcome-joint.log`。模型为确定性 fixture，不是外部模型/browser 验收。
- 随后补主动暂停分支，使用用户 HTTP 暂停、主对话流控制/Worker 真实心跳观测，保留同一原生工具批次及子任务结果，重建后继续。此版联合测试二进制被 Application Control 拒绝启动，`.tmp/goal-g422-subtask-pause-fault-joint.log`；尚未实际执行，不记作通过，不改名换路径或修改策略绕过。前一轮故障恢复结果不替代此分支。
- G4.23 开始原生工作区工具适配。固定 SDK 的 ExecCommandTool 是带自定义构造器的 FunctionTool 子类，Filesystem 的 SandboxApplyPatchTool 是 CustomTool，不应直接套用普通函数工具的克隆/JSON 参数假设。先验证原生工具绑定与现有审计审批，再完成受控执行环境、工作文件与快照接线。SDK Docker 额外依赖当前未安装，默认实现自动拉取镜像且没有平台资源限制，不能直接启用或因此宣称工作区已可用。

#### G4.23 原生工具复制与补丁审计（2026-09-08，工作区未贯通）

- 修复原生 ExecCommandTool 被 dataclass `replace` 重新调用自定义构造器而失败的问题，使用 SDK 支持的实例复制保留会话绑定；工具 guardrail 同步采用实例复制。此前原生专项2项失败、修复后7项通过的报告均保留；本批完整 SDK **892 项通过，155.03s**，`.tmp/goal-g423-sdk-all.xml`。
- 新增 `native_patch_tool.py`，模型侧保留 SDK 原生 CustomTool 及补丁语法，执行仍由 SDK WorkspaceEditor 负责。Go 审计参数精确绑定原文，要求显式审批和可靠审计，不允许原生自动批准回调绕过用户决定；数据保护在登记审批前检查，校验原工具/调用/参数哈希，阻止未批准、并发重复和已完成调用再次执行。失败、取消、超限或丢失完成回执不返回成功、不自动重放。
- SDK Runner/RunState 原生批准与拒绝、序列化后重建工具、路径越界、审计绑定、异常回执及并发调用专项 **27 项通过，4.00s**，`.tmp/goal-g423-native-patch-fixed.xml`。首次为23通过/1失败，断言错误地要求 SDK 给新文件附加末尾换行；按原生编辑器实际结果修正夹具，没有改 SDK 行为，`.tmp/goal-g423-native-patch-first.xml` 保留。
- 扩大完整 SDK 为 **911 通过/1 失败，222.91s**，`.tmp/goal-g423-sdk-patch-all.xml`。唯一失败是既有 stdio MCP 连接10s超时；同一用例未改代码/超时复测 **1 项通过，5.26s**，`.tmp/goal-g423-mcp-recheck.xml`。随后同版完整复测 **912 项通过，191.95s**，`.tmp/goal-g423-sdk-patch-rechecked.xml`；XML 核对无失败、错误或跳过。不把单项复测拼成全量，不删除首次失败记录，超时根因尚未确定。
- 接入与验收约束记录在 `agent-native-workspace-contract.md`。以上是原生工具层适配；受控会话/快照、三生产入口、二进制工作文件及用户操作仍未贯通，不能称为工作区/Shell 已对用户可用。G4.22 主动暂停联合的 Application Control 阻塞和最终全局 review 未完成状态不变。

#### G4.24 受控持久执行环境基础（2026-09-08，尚未注册用户入口）

- 在现有 Go `scriptsandbox.OCI` 内增加 `workspace.go`：独占 UUID 会话、创建/查找/重连/删除、受限命令执行与二进制 stdin/stdout。不引入另一套 Agent 循环，沿用固定镜像、无网络、非 root、只读根、能力限制及 CPU/内存/进程/磁盘配额；无宿主挂载，不自动拉取镜像。工作区身份句柄仅为内部记录，不是用户或 SDK 自带的授权凭据。
- 重连及命令执行前后核验真实容器 ID、会话/归属/策略标签、镜像、用户、namespace、挂载与资源配置。丢失/停止的环境不静默重建为空目录；创建回执丢失可按 Runtime 原身份只读找回，核验失败不能启动或删除。命令超时、取消、输出溢出或传输失联后终止经核验的容器，清理不确定时明确失败，不声称后台进程已停止或未保存文件仍存在。
- 复用原命令执行器增加受限 stdin 传输，保持最小引擎环境；修复原 CPU 配额校验漏拒 NaN/Inf。模型 argv 在容器和 env 选项之后，拒绝命令名伪装成参数或环境赋值，保留程序真实非零退出码；这不是写入成功回执或工作文件已发布证明。
- 新引擎专项通过 **3.206s**，`.tmp/goal-g424-native-workspace-engine-first.log`。增加回执丢失找回和运行中环境消失检查后，沙箱整个包回归通过 **5.105s**，`.tmp/goal-g424-sandbox-package.log`，19个顶层测试通过、3个真实 OCI integration 测试跳过、无失败。显式关闭真实 OCI integration 开关，不算实际环境验收。上述新测试均使用模拟引擎，没有执行容器或操作用户环境。Go 全后端 `build ./...` 退出0，`.tmp/goal-g424-go-build.log`；构建不替代全包业务回归。
- **尚未完成**：Runtime 执行身份/租约持久化与授权、过期会话和孤立容器回收、SDK BaseSandboxClient/Session 适配、原生工具装配、PTY 会话、文件快照/二进制存储发布及三入口/UI 接线。当前未将新接口注册到生产工具目录，也未启动它；最终全局 review 状态不变。

#### G4.25 工作区归档、恢复与旧脚本导出修正（2026-09-08，未接生产工作区入口）

- `workspace_archive.go` 增加受限、规范化 tar 快照：文件/目录、二进制、权限、逐文件及整体 SHA-256；拒绝穿越、链接/设备、大小写路径冲突、损坏/截断/尾随数据和文件/目录/字节超限。Go 不把快照解压到宿主目录。恢复先校验完整快照及选定哈希，只允许空工作区或完全相同的内容，不覆盖不同的已有内容；同一容器操作串行，等待者可取消，结束后释放锁记录。
- **保留无效原型记录**：最初用 `pause + docker cp` 的模拟测试曾通过（`.tmp/goal-g425-workspace-snapshot-first.log`，3.630s），但核对官方文档发现 tmpfs 不能按该方式复制，暂停的容器也不能执行 `exec`。该实现已移除，新夹具遇到 `cp/pause/unpause` 直接失败，原通过记录不算真实引擎兼容证明。[Docker cp 限制](https://docs.docker.com/reference/cli/docker/container/cp/)、[Docker exec 暂停限制](https://docs.docker.com/reference/cli/docker/container/exec/)。
- 改为 Go 嵌入可信 `workspace_transfer.py`，在运行中的固定容器内经清空环境、非 root Python 执行一次归档/恢复。导出前停止本容器的其他活跃进程，完成或异常后只恢复本次停止的进程；使用 pidfd 防 PID 重用误操作，保留用户原来暂停的进程，恢复失败仍尝试其余进程。受限保留48个进程引用，不够时明确 busy；运行环境需支持 Linux pidfd，真实兼容性未验收。传输/恢复回执丢失、部分写入、损坏输出、取消或恢复进程失败都不自动重放，终止已核验的容器；无法确认清理时单列错误。
- 工作区监督进程增加孤立子进程回收，并把精确 entrypoint/命令纳入重连核验，策略哈希升级为2；旧策略句柄不能当新版使用。没有改变网络、只读根、非 root、能力集、tmpfs noexec 或原配额。
- 同一检查确认旧 `Sandbox.Execute` 在脚本作为 PID1 退出后再从 `/output` tmpfs `cp` 导出产物的生命周期错误。现为创建并取得完整容器 ID、启动监督进程、受限执行脚本、在运行容器内归档产物、完整校验后提取、最后按确切 ID 清理。创建身份不明不操作/删除未知容器；启动/导出/归档失败无产物回执，传输未知不继续导出。原 Skill/输入只读挂载及隔离配额保持。真实进程配额 fixture 增加自行终止/等待其测试子进程，保留原配额断言，避免占满进程槽后无槽启动导出助手；该真实 OCI 用例仍未运行。
- Python 辅助程序专项 **33 项通过，0.64s**，`.tmp/goal-g425-transfer-helper-reviewed.xml/log`；首次为26通过/7个临时目录权限错误，`.tmp/goal-g425-transfer-helper-first.xml/log` 保留。同一已核定 pytest 目录经权限审查复跑，未改路径或安全策略。进程扫描/信号测试全为注入的模拟对象，未向宿主进程发送信号。
- 真实 Go/Python 文件归档往返验证中文路径、二进制、空目录、权限及重复恢复，**1 项通过，1.579s**，`.tmp/goal-g425-transfer-cross-language-reviewed.log`；第一次默认权限拒绝 Python 写 Go 测试目录，原命令审查后通过。该测试仅在临时目录调用纯归档函数，不启动 OCI 或调用辅助程序的主进程入口。最终沙箱整个包 **29个顶层测试通过、4个真实 OCI 测试跳过、0失败，6.749s**，`.tmp/goal-g425-sandbox-final.log`；新增真实 OCI 快照/恢复/后台进程释放用例保留未验收。
- 相关 Runtime 审批、原脚本版本/重建、取消及租户隔离专项通过 **45.727s**，`.tmp/goal-g425-script-runtime-regression.log`。Go 全后端 `build ./...` 退出0，`.tmp/goal-g425-go-build.log`。本版完整 SDK **945 项通过，211.00s**，`.tmp/goal-g425-sdk-all.xml/log`，不是外部模型 E2E 或全 Go 业务回归。此前前端348项属于 G4.21 本版前端未改，不冒称重新进行了浏览器验收。
- **下一步/仍未完成**：Runtime 执行归属与租约持久化、快照版本/二进制存储与并发发布、过期/孤立环境回收；SDK BaseSandboxClient/Session、PTY 及三个生产入口、工具目录/UI。固定 SDK 默认恢复会先清空工作区，辅助脚本默认按可执行文件调用，不能直接套用到本平台保留现有文件和 tmpfs noexec 的策略上；接线时需用实际 SDK 验证这些适配边界，不能放宽策略或伪称原生工具已可用。G4.22 主动暂停联合的 Application Control 阻塞、实际 OCI/网关/浏览器及最终全局 review 仍未完成。Goal 保持 active；不 push、不部署、不操作用户数据库/作品或8860/8880。

#### G4.26 执行工作区租约与持久快照（2026-09-08，尚未接生产工具）

- 源码 schema 56 增加执行工作区租约与不可变二进制快照。主对话绑定当前 dispatch generation，后台/状态化绑定实际 attempt/token/lease；每次存取重新校验原发起人及当前权限，不因同属作品而共享执行授权。只保存持有密钥哈希，过期或换代后旧持有者失效；这只保护数据库操作，尚不是运行中 OCI 命令的跨进程终止保障。
- 快照按精确版本和 SHA-256 保存、读取，复用规范化 tar 校验；幂等重试也复查已存正文与文件清单完整性，不自动读取最新版本或恢复为空目录。新增执行级数量/容量上限，纳入工作区存储配额。作品删除预览纳入快照数量与版本哈希，确认后清理正文并关闭租约；工作区删除已接相同清理，但仍需新增原生快照的整工作区删除专项。关闭记录不代表 OCI 环境已经清理。
- Runtime 租约、快照、权限撤销、暂停/取消、三执行身份重建、旧租约/调度代次失效、配额、作品删除和迁移扩大回归退出 0，**94.639s**，`.tmp/goal-g426-workspace-store-regression.log`。55 -> 56 迁移仅在临时数据库验证备份与原执行不变。首次编译存在配额 helper 名称错误，修复后通过，原失败日志 `.tmp/goal-g426-workspace-store-build.log` 保留。全后端 `build ./...` 退出 0，`.tmp/goal-g426-go-build.log`；不是完整 Go 业务回归或真实 OCI 验收。
- **仍未完成**：Runtime/OCI 环境绑定、持久操作互斥与回收；SDK BaseSandboxClient/Session、交互式 PTY、快照恢复与原生工具装配；三生产入口、文件发布/Skill 作者链及前端操作贯通。随后仍需同版全量回归、真实环境验收和最终全局 review。最新 SDK 945 项、前端348项是此前版本的分层证据，本批没有重跑或扩大为整体完成证明。
- 当前剩余工作暂估 **20–35 小时有效工作时间**：纯开发12–20小时、同版自测/回归5–8小时、最终全局 review 与修复3–7小时。此为当前已知缺口的粗估，不是保证交付时间；真实 OCI、Responses/compact 网关及浏览器验收条件的等待另计，不能用外部阻塞结束目标。Goal 继续 active；未 push、部署或操作用户作品/数据库、8860/8880。

#### G4.27 Runtime 与 OCI 生命周期桥接（2026-09-08，编译通过、运行回归受阻）

- 源码 schema 57 增加私有环境绑定及持久在途操作。`NativeWorkspaceManager` 复用已有 OCI 的创建、精确身份重连、快照导出和删除接口，编译期校验实际 `scriptsandbox.OCI` 满足接口；没有另造 Agent 循环。环境 ID 在调用引擎前入库，与公开执行工作区 ID 分开；公开结果不含容器 ID、所有权哈希或连接配置。构造 manager 不启动环境、定时清理或用户服务，尚未注册生产 HTTP/SDK 工具入口。
- 数据库中的在途操作用于跨 Runtime 实例互斥，不因操作时间到期就放行另一轮；原执行失效由监测取消上下文，迟到回执不能覆盖正在清理或已关闭的绑定。创建回执丢失时保留不确定状态，清理只能通过原身份核验得到确切句柄，不把查不到容器当作已删除。未知创建/清理仍计入工作区环境上限。内部有界 Sweep 只供非委派的服务清理身份调用，未启用后台清理循环。
- 快照导出接入已有版本化持久存储，禁止另一路保存跨越在途操作。已完成的快照请求重试读取原版本并复查完整性，不重新导出当前内容；参数错误在导出前拒绝。有旧快照而无环境时拒绝隐式创建空目录；显式选择快照重建/恢复的管理器路径仍待接通。
- 静态复核补了两个失败分支：已验证完成的只读导出遇到数据库/配额失败，或辅助程序明确报告无写入的忙碌/归档拒绝时，保留现有工作文件，不自动进入清理；OCI 已确认终止的 `WORKSPACE_TRANSFER_UNCONFIRMED` / `WORKSPACE_COMMAND_INTERRUPTED` 与 `WORKSPACE_CLEANUP_UNCONFIRMED` 分开记录，避免已删除环境永久占用容量。未知副作用仍不自动重放。
- 新增 **10 组顶层测试**，覆盖三种执行身份、关闭并重建 Store、精确二进制快照/回执、双 Store 互斥、租约轮换、创建/删除回执丢失、错句柄/空回执、删除时迟到快照、整工作区清理、56 -> 57 备份迁移及只读失败保留文件。**这些新增行为尚未通过修正版运行验收。** 首轮12组顶层测试失败（40.437s，`.tmp/goal-g427-native-environment-first.log`），原因是抽取 UUID helper 时局部变量遮蔽数据库错误变量，首次预留返回旧的 `sql.ErrNoRows`；已修正生产变量作用域。
- 修正后的原命令在默认权限及经审查权限下均被 Windows Application Control 拦截 `runtime.test.exe`，最后记录 `.tmp/goal-g427-native-environment-fixed.log`。没有改名、换目标路径、改系统策略或以其他执行方式绕过被拒测试。之后继续修改源码并做不运行测试程序的检查：全后端 **`build ./...` 退出0**（`.tmp/goal-g427-go-build-final.log`），Runtime/沙箱 **`go vet` 退出0**（`.tmp/goal-g427-go-vet-final.log`）；编译和静态检查不能替代上述未通过回归。一次 apply_patch 读取测试文件临时拒绝，随后同文件普通读取成功、原地重试编辑成功，没有改 ACL。
- **下一步仍按完整原范围**：补齐精确快照选择/恢复、持久命令与 PTY 会话及平台审批绑定；接 SDK BaseSandboxClient/Session 和原生 Filesystem/Shell/Skills、三生产执行入口、文件发布/安装链及前端；恢复同版运行回归与真实环境验收，再做最终全局 review。生产策略/清理调度、SDK noexec 助手适配和失联恢复不能凭本批生命周期代码算完成。Goal 保持 active；未 push、部署、操作真实 OCI/用户项目数据库、8860/8880。

#### G4.28 精确快照恢复与 SDK 原生 Snapshot 适配（2026-09-08，SDK 全量通过、Go 运行验收受阻）

- 源码 schema 58 增加不可变恢复请求记录，绑定执行工作区、原环境 ID、确切快照版本/哈希和在途操作。`NativeWorkspaceManager.Restore` 在产生环境副作用前校验当前权限、租约及快照完整性；只有没有环境或原环境已确认关闭时才建立新私有环境 ID，现有目录恢复只接受空目录或完全相同的内容，不先清空工作区。恢复回执与环境状态同事务提交；已完成请求的重试只重连其原环境，不重新恢复内容。重连领取与旧回执核验同事务，避免并发替换环境把旧请求转向新目录；失败/未决请求不自动重放，迟到回执不能覆盖新绑定。
- 单独提供不推进持久版本的 `Export`，对应 SDK 的 `persist_workspace -> Snapshot.persist` 两步生命周期，避免 SDK 随后保存时重复推进版本。新增按请求 ID、父版本和内容哈希读取确切保存回执的 Store 接口；缺失不回退至最新版本，存在时重新校验正文及清单。管理器组合式 Checkpoint 仍保留原用途。新增 **7 组 Go 顶层测试**覆盖三执行身份、旧版本恢复、重建后的回执、恢复冲突保留现有改动、确认清理后的新环境、失联恢复、权限/哈希/请求拒绝、57 -> 58 备份迁移及导出/保存分离。尚未运行这些新增 Go 用例，不绕过 G4.27 的 Application Control 阻塞。
- 复核实际固定 SDK 0.21.1：`SnapshotBase` 的公开 persist/restore/restorable 协议、类型注册与序列化、会话 Dependencies，以及 SDK 原生 `persist_snapshot` 的流关闭/指纹行为。新增 `RuntimeWorkspaceSnapshot` 适配该公开协议，使用一个明确的可序列化版本引用，确认保存后才推进；执行 ID 不可变，状态不包含传输对象、凭据、宿主路径或正文。传输通过当前可信会话依赖重绑定并核对执行 ID。持久化结果不明时保留原请求/父版本/哈希，重建后只查该回执；不得因已存在旧引用而回退旧版或最新版。读写大小和 SHA-256 有界检查，支持短分块读取，同绑定串行保存，取消保留待核对请求。任意依赖/HTTP 异常不经异常链暴露原始凭据内容。
- SDK 专项修正版 **12 项通过，7.26s**，`.tmp/goal-g428-native-snapshot-fixed.xml/log`；验证实际 SDK Snapshot 注册/JSON 重建、公开依赖和原生快照持久化生命周期，存储传输和工作区导出是隔离替身，不是真实 Go HTTP 或 OCI 联跑。首次为11通过/2个 setup/teardown 错误、24.90s，原因是超大参数被 pytest 放入测试名，导致 Windows 环境变量超过32767字符；只改短测试 ID，保留18MiB超限断言。原失败 `.tmp/goal-g428-native-snapshot.xml/log` 体积较大，排查时只提取错误类型，不整份输出。之后追加依赖工厂异常不泄密断言，纳入本版完整 SDK 回归。
- 本轮最终全后端 **build 退出0**（`.tmp/goal-g428-go-build-final.log`），Runtime/沙箱 **go vet 退出0**（`.tmp/goal-g428-restore-vet-final.log`）。这些检查不执行被拒的 Go 测试程序；没有改名、移动目标或改变系统策略以绕过限制。本版完整 SDK **957 项通过，221.44s，0失败/错误/跳过**（`.tmp/goal-g428-sdk-all.xml/log`），使用既有核定 `.tmp/goal-g421-audited-native-tmp` 目录及隔离替身运行，终态退出0。它不包含新增 Go 恢复逻辑的运行证明，也不是真实 OCI、外部模型或浏览器 E2E。
- **仍未完成**：实际 Go HTTP/Sidecar 传输适配、BaseSandboxClient/Session、受平台审批绑定的命令和 PTY、SDK noexec 助手适配、三生产入口/工具目录、工作文件发布/Skill 安装链及用户 UI。Snapshot 适配已经有分层验证，但尚未装配到生产 SandboxAgent；不能据此声称工作区用户闭环已贯通。完整同版回归、真实环境验收和最终全局 review 继续保留。未 push、部署、操作用户作品/数据库、真实容器或8860/8880；Goal 保持 active。

#### G4.29 私有工作区 HTTP 与 SDK Snapshot 传输（2026-09-08，SDK 全量通过，Go 联合运行待验收）

- 新增实际 Go 内部路由：当前执行租约发现、预留/续租、租约核验、环境建立/精确恢复/只读导出，以及二进制快照保存、确切版本读取、确切请求回执读取。统一绑定已有内部服务认证、真实 AgentActivity、主对话 dispatch generation 或原 worker attempt/token，以及私有持有密钥/租约代次；在读取上传正文前核验执行权限。当前租约发现只返回本执行的公开 ID/代次/期限/快照头，不返回密钥哈希或容器句柄，也不自行接管租约。
- 环境操作使用管理员沙箱策略约束的 manager，资源限制取管理员值与平台固定上限中更严格者。在开始、运行监测及提交回执时重查权限、租约和当前策略，撤权或策略变化不能返回 ready；监测取消导致的通用取消错误不会掩盖已确认的撤权原因，也不覆盖已确认清理的引擎错误。构造/注册路由不启动引擎、后台清理或用户服务；尚未配置生产启动入口。
- 快照 HTTP 使用最多 18 MiB 的原始 tar，不走 JSON/base64；拒绝含糊或编码后的媒体头、非规范数字/重复绑定头、超限正文、原始 SHA-256 不符及非规范归档。保存回执只包含公开执行工作区 ID、确切版本和哈希；读取必须提供确切版本/哈希，请求回执缺失明确返回 null，不回退最新版本。环境导出仍为 SDK 两步持久化的第一步，不隐式推进数据库版本。
- 新增 Sidecar `NativeWorkspaceHTTPTransport` 和 BackendClient 私有传输方法：固定执行上下文，切换项目/执行/attempt token 后禁止复用；接管必须绑定已发现的逻辑工作区 ID，续租不能替换绑定。凭据仅在可信运行时请求头，禁止自动重定向与隐式环境代理；控制请求/响应和二进制均有大小上限。检查状态、媒体类型、重复响应头、长度、会话、版本和哈希；包括 HTTP 错误正文读取失败在内的异常均返回静态未确认错误，不保留原始凭据异常链。修复了畸形回执类型处理及无时区租约时间的校验缺口。
- 新增 **35 项 Python 传输测试**，使用真实随机 loopback HTTP 连接验证三个执行身份、二进制、代理/重定向不外发凭据、租约续期/换代绑定、畸形回执/超限/哈希错误，以及实际 SDK Snapshot JSON 重建后按原请求核对丢失保存回执、不重放 PUT。加上原 Snapshot 专项共 **47 项通过，10.31s**（`.tmp/goal-g429-native-transport-first.xml/log`）。HTTP 服务为确定性协议夹具，二进制用例明确为传输字节，不模拟 Go SQL，也不冒充 Go HTTP/OCI 联跑或归档格式兼容证明。
- 新增 **5 组 Go HTTP 顶层测试**，使用真实路由、认证及临时 Store，覆盖规范化 tar 保存/精确读取/恢复幂等、认证先于正文读取、非规范归档/重复或超限请求拒绝、未配置引擎、策略关闭/收紧和创建中撤销。G4.27 同一 Application Control 拒绝仍未解除，**未运行这些新增 Go 用例**，未通过改名/路径/其他执行方式绕过。最终 Runtime/HTTP/沙箱 **go vet 退出0**（`.tmp/goal-g429-native-http-vet-final.log`），全后端 **build 退出0**（`.tmp/goal-g429-go-build-final.log`）；不是 Go 业务运行验收。
- 本版完整 SDK **992 项通过，194.30s，0失败/错误/跳过**（`.tmp/goal-g429-sdk-all.xml/log`），沿用既有核定的项目内 pytest 临时目录、模拟模型和依赖。最后仅追加 Go 侧取消原因/HTTP 错误状态及请求头检查，并已重做 Go 静态检查和构建；没有把 SDK 通过扩大为 Go/真实 OCI/模型/浏览器验收。`git diff --check` 退出0。
- **当前仍未完成**：BaseSandboxClient/Session、受平台工具审批约束的命令/PTY及 noexec 原生助手适配、三生产 Runner/工具目录、工作区文件发布/Skill 安装链和用户 UI、生产引擎/清理调度。SDK 与 Go 传输各层已实现，仍须真实联合运行验证，不能据此勾选 GAP-08。继续原范围的同版全量/真实环境验收和最终全局 review；Goal active，未 push/部署/操作用户作品数据库、8860/8880、测试机或真实容器。

#### G4.30 已审批单次命令回执（2026-09-08，尚未接生产会话）

- 源码 schema 59 增加私有命令账本，绑定原 SDK 调用/参数哈希、执行身份、工作区租约、确切环境和在途操作。必须先取得平台明确审批与正在运行的调用；只读子任务不能执行命令。输出容量先预留，结果、哈希及环境状态同事务提交；失败、在途、已删除、损坏或参数变化的请求不自动重放。完成工具审计必须存在匹配的已确认命令回执，作品/工作区删除清除输出并保留不可重放的身份记录。
- 新增内部 commands 路由、严格校验的 Sidecar 二进制命令传输和调用级 ContextVar 绑定。沿用 SDK 原生 ExecCommandTool 的参数及命令准备，不重写 Agent 循环；强制审批、可靠审计、零自动重试、平台数据保护。命令级输出上限单列，普通控制接口的较小响应上限不放宽。超时仍受既有沙箱策略约束。这是单次命令基础，不是 PTY 或 BaseSandboxSession，更未装配到三个生产 Runner。
- 本批自查发现审批先于原生工具输入 guardrail，敏感参数可能在执行拦截之前被登记为待审批。现对原生命令在审批登记及直接调用前检查同一平台数据策略，缺少策略即拒绝装配；测试覆盖敏感值和超限参数，确认无审批登记、无执行。另修正并发 Go 测试在提前失败时未释放夹具协程的清理问题。
- 首次 SDK 专项为69通过/2失败，`.tmp/goal-g430-command-focused.xml/log`：一项是旧 guardrail 测试未考虑必须审批，一项是 stdio MCP Windows 命名管道权限拒绝。修正边界与断言后，同一组本地隔离测试经权限审查运行，最终 **73 项通过，13.64s，退出0**，`.tmp/goal-g430-command-focused-fixed.xml/log`。使用真实 SDK 和随机 loopback 协议夹具，模型/命令引擎为替身；没有访问真实模型、OCI、用户作品或测试机。
- 新增5组 Go 顶层测试覆盖三执行身份、审批/参数/路径拒绝、并发互斥、未知结果不重放、配额、撤权/删除与58到59迁移。第一次 vet 发现测试使用了错误的 CancelAgentToolCall 参数签名，已修正。最终 **go vet 退出0**（`.tmp/goal-g430-command-vet-final.log`），全后端 **build 退出0**（`.tmp/goal-g430-go-build-final.log`）。没有运行被 Application Control 拒绝的 Go 测试，不能称这5组已经通过。
- 本批没有重跑 SDK/Go/前端同版全量，也未完成全局 review。当前后续工作以本文件顶部固定交付组为准，撤回不可靠的时间估算；保留原完整范围和回滚边界，Goal active，未 push、部署、启动真实引擎或切换用户环境。

#### G4.31 SDK 会话生命周期适配（2026-09-08，文件提供方仍未贯通）

- 新增 Runtime `Probe` 与 `Shutdown` 及相应内部 HTTP/Sidecar 传输。探测只核验已有环境，不因 absent/closed 隐式新建；关闭复查实际执行租约、策略和当前操作互斥，真实删除回执之后才记 closed。失败或未知结果保持 blocked、不自动重放，已确认关闭可重复读取同一关闭状态。新增3组 Runtime 顶层测试及1组 HTTP 测试，覆盖三执行身份、保存后关闭/精确恢复、错租约、忙操作及未知删除；本批不改变 schema 59。
- 新增 `RuntimeSandboxOptions`、`RuntimeSandboxState`、原生 SnapshotSpec、`RuntimeSandboxClient` 和抽象 `RuntimeSandboxSession`。复用 SDK 的会话包装/事件、Dependencies、Manifest 文件物化、命令准备、Snapshot 保存与状态序列化。重新绑定当前执行租约，状态不带传输对象/密钥/宿主授权；拒绝任意快照提供方、环境变量、用户切换、端口、挂载和本地源。当前 manifest 接受有界固定文本文件/目录，二进制工作文件走 I/O/快照，不把 SDK 无法 JSON 序列化的 File 原始字节当作可恢复状态。
- 恢复使用确切快照及原恢复请求，不调用 SDK 默认清空目录的方法；保存或恢复回执丢失保留原请求供重建核对。默认快照指纹优化不用来替代 Go 规范归档哈希及非覆盖恢复校验。`read/write` 和受限 SDK 文件助手协议保持抽象，没有以空实现装配生产或回退到宿主文件系统；客户端会拒绝未实现文件 I/O 的提供方。单次命令已接上一批审批 ContextVar 与 HTTP 回执，PTY、文件桥接和三个生产 Runner 仍未贯通。
- 验证实际 SDK 清理流程发现 `_SandboxSessionResources.cleanup` 在 stop 失败后仍调用 shutdown/delete，Runner 还会只记录警告并返回模型 final_output。适配层在预清理前设置保存保护，保存/导出/后续 hook/启动不确定时禁止自动销毁；确认关闭后的重复 aclose 不再重复保存。新增 `require_confirmed_cleanup` 供生产入口在接受工作区交付前检查，**这项检查尚未接入三个生产入口，不能把 Runner 返回文本当作已保存证明**。
- 首次专项44通过/1失败，`.tmp/goal-g431-native-session-first.xml/log`，原因是测试用了错误的 SDK Environment 字段形状，修正后扩展专项87项通过，13.19s，`.tmp/goal-g431-native-session-fixed.xml/log`。后续真实 loopback HTTP 的 SDK 创建/保存/关闭/换租约/确切恢复验证通过。实际 SandboxAgent + Runner 用例初版22通过/1失败，`.tmp/goal-g431-runner-lifecycle.xml/log`，暴露上述 SDK 吞掉清理异常行为；按真实契约添加确认检查后，最终会话专项 **23项通过，7.24s**，`.tmp/goal-g431-runner-lifecycle-fixed.xml/log`。模型、文件提供方和环境为隔离替身，不是实际 Go/OCI 文件联跑。
- Go 首次 vet 发现新测试引用了错误的快照字段，修正为真实嵌套字段，同时核正 stateful fixture 模式和 HTTP 错租约403断言。最终 **go vet 退出0**（`.tmp/goal-g431-native-session-vet-final.log`），全后端 **build 退出0**（`.tmp/goal-g431-go-build-final.log`）。没有尝试执行被 Application Control 拒绝的 Go 测试程序；这些新增 Go 行为只有构建/静态证据。早一版本完整 SDK **1030项通过，166.34s**（`.tmp/goal-g431-sdk-all.xml/log`）；加入实际 SDK 运行管理器清理保护后的最终同版全量另记，不以该较早结果替代。
- G4.31 最终同版完整 SDK **1035项通过，180.90s，0失败/错误/跳过，退出0**（`.tmp/goal-g431-sdk-all-final.xml/log`），已核对 XML。沿用项目内已核定临时目录，经命名管道/临时文件执行权限审查运行；全部使用隔离模型和依赖，没有真实 API、OCI 或用户环境操作。所有本批测试进程已退出，`git diff --check`退出0。此结果覆盖新增 SDK 生命周期，但不替代未运行的 Go/OCI 联合验收或用户前端闭环。
- 下一实现位置已具体化：为抽象会话接 Runtime 受限文件读写/目录/元数据及 SDK 助手协议，保留原生 WorkspaceEditor/Manifest 算法和审批归属；随后完成 Skills/Shell 能力装配和 D3 三入口/前端。不再重新开发本批生命周期，D1仍未完成，Goal active，未 push/部署/操作用户环境或真实 OCI。

#### G4.32 OCI 受限文件原语（2026-09-08，尚未接 Runtime/SDK 文件提供方）

- 在已有 Go `scriptsandbox.OCI` 中增加 `FileWorkspace`，仅暴露 read/stat/list/write/mkdir/remove/chmod 七种定型操作。复用现有容器身份核验、串行操作锁、固定非 root 解释器/清空环境、无宿主挂载和 noexec；请求数据只走有界 stdin，不能选择程序或工作根。不注册模型工具或开放新生产路由。
- 复用原受控归档助手的路径和树检查。文件正文最多16MiB、线路最多24MiB，同时受当前磁盘/文件数/目录数限制；拒绝绝对路径、穿越、非法便携路径、大小写别名、符号链接/特殊文件及文件目录类型错误。stat/list只扫元数据，不逐次读取所有文件正文。
- 写入在同目录独占临时文件中完成、同步并核对实际字节后原子替换，事先保留新旧内容同时存在的峰值空间；写入失败不先截断旧文件。读取/写入回执带确切SHA，Go检查操作、路径、大小、元数据及正文。失败可能发生在替换后或恢复进程时，因此不宣称所有失败都已回滚；不明回执会按既有策略终止确切核验环境，无法确认终止则单独报错，绝不重放或回退宿主执行。
- 文件成功回执必须在本次停止的容器进程全部恢复后才发出。新增55项Python测试与原33项归档测试合跑，**88 passed in 3.70s**（`.tmp/goal-g432-file-helper-fixed.xml/log`）；实际读写仅在隔离临时目录，进程/固定根使用替身，不扫描或发信号给宿主进程，不证明Linux实际pidfd/chmod/OCI行为。先修正pytest保留参数名及测试替身递归两个测试代码错误；默认权限在已核定临时目录清理时遭WinError5，审核后在原路径运行通过，没有改ACL/系统策略。
- 新增5组Go契约/传输/错误测试。`go vet`四相关包退出0（`.tmp/goal-g432-file-vet.log`）、全后端`go build ./...`退出0（`.tmp/goal-g432-go-build.log`）；**Go测试未运行**，仍受既有Application Control阻塞，不将编译当作行为通过。本版完整SDK **1090 passed in 193.03s，0失败/错误/跳过，退出0**（`.tmp/goal-g432-sdk-all.xml/log`），已核对XML；沿用获准隔离临时目录和模拟依赖，没有真实模型/OCI调用。所有本批测试/构建进程均已退出，`git diff --check`退出0。
- 下一步直接接该文件协议的Runtime执行授权/审批归属与SDK具体会话实现，再接原生能力和D3生产入口；不重复本批文件原语，也不把所有SDK助手命令当作免审批的任意Shell。D1仍未完成，最终全局review尚未开始；Goal保持active，未push/部署/操作测试机、用户数据或8860/8880。

#### G4.33 Runtime 文件授权与具体 SDK 会话（2026-09-08，生产能力装配仍未完成）

- 沿用G4.32的定型文件协议接入`NativeWorkspaceManager.File`和私有`POST .../native-workspaces/{session_id}/files`。在读取大请求前校验服务身份/当前执行/租约，文件操作仍要求当前管理员沙箱策略、资源上限和确切环境。修改操作绑定已启动、明确批准并消耗审批的`runtime:apply_patch`及原SDK调用ID/参数哈希；只读子调用、旧调用、变更后的参数或配置不能授权写入。唯一不需要补丁授权的mkdir是已存在固定根的`mkdir(".", parents=True)`，不是通用初始化写入授权。
- 原生补丁目录描述补齐到Go统一工具注册表；维持SDK CustomTool原始补丁语法、WorkspaceEditor解析与多文件编辑，平台包装在调用期间通过ContextVar传递精确审批归属，不序列化该授权上下文。无活动补丁调用的文件写入或任意SDK helper Shell都不放行；SDK固定chmod助手仅映射到同一受权文件原语。
- 源码schema升至60，新增`native_workspace_file_operations`保存每次修改的有界元数据回执，不复制文件正文。请求ID与内容哈希/调用身份绑定；已确认的同一请求只返回旧回执，运行中/失败/损坏请求不重放。开始与回执提交沿用环境互斥、权限监测和同一事务，未确认文件修改阻止整个原生补丁完成；SDK零修改操作可以没有修改回执，不能用这个例外忽略实际已发出的写入。元数据预留计入存储配额，租户/调用数量受限，作品删除预览哈希、删除清理和环境回收覆盖这些记录。**没有打开或迁移用户数据库**。
- 新增`RuntimeSandboxFileSession`，通过实际HTTP传输实现SDK read/write/ls/mkdir/rm、二进制流和SDK文件元数据；SDK路径规则与Go真实目录约束共同生效。未知文件回执或取消保留`pending_file`请求ID/哈希，阻止后续I/O、快照交付和自动销毁，重建也不得静默丢弃待核对状态。已确认的文件不存在错误维持SDK FileNotFound语义。
- 自查修复保存竞态：SDK开始清理/保存快照后仍接受新文件或Shell操作，可能使已保存版本落后于环境最终内容。现在从保存开始到关闭禁止新增操作，需要新的恢复会话才能继续编辑；正常SDK会话重建仍保留原确切快照和文件字节。
- **94项专项通过，15.12s，0失败/错误/跳过**（`.tmp/goal-g433-native-files-final.xml/log`，XML已核对）。包含真实SDK审批批准/拒绝及RunState/文件会话序列化重建、实际临时文件的Skill目录/移动/删除/多文件编辑、二进制保存恢复、失联/取消不重放、保存竞态；另有SDK Client经随机回环HTTP读写临时文件、保存、换持有者/租约代次后重建。服务器/审批/引擎是隔离契约夹具，**不是Go Store或真实OCI验收**。初轮测试修正SDK旧Agent实例恢复、SDK特定路径错误类型，以及Go只读子调用关系表位置的错误假设，没有删除对应验证。
- 新增6组Runtime和2组HTTP测试源码，覆盖三模式、旧审批/只读子任务、回执重开与损坏、暂停中的在途补丁、删除迟到结果和v59迁移备份；Go编译/静态检查均退出0（`.tmp/goal-g433-file-vet-final.log`、`.tmp/goal-g433-go-build-final.log`）。**这些Go测试未运行**，保持原Application Control阻塞，不绕过系统策略或另起运行器。本版完整SDK **1141 passed in 184.10s，0失败/错误/跳过，退出0**（`.tmp/goal-g433-sdk-all.xml/log`），XML已核对；所有测试/构建进程已退出，`git diff --check`退出0。
- 下一实现位置是可信manifest/Skill资源与SDK Filesystem/Shell/Skills能力装配，以及剩余noexec助手支持。本批具体文件会话支持空manifest起步并通过批准工具写文件，**不意味着非空manifest的工具外写入已经获授权**；不能靠直接传入内联资源或去掉审批完成这一步。随后完成D2/D3、同版验收及最终全局review。Goal active，不push/部署/操作测试机、用户数据或8860/8880。

### G4.34 可信初始资源与非空 SDK Manifest

- Runtime schema 61 增加执行内初始化清单，不修改已有补丁审批账本或伪造模型批准。输入只接受 Skill 版本/包哈希、项目文件确切版本/内容哈希；复用执行冻结目录、Skill 原始资源哈希校验及已有项目文件历史。Skill 装入 `.skills/<name>/`，项目文件不能占用该空间；资源引用没有宿主路径，单份清单有文件数、目录数、字节和元数据上限。
- 在环境/快照存在之前确定清单。Runtime 仅授权清单所需的精确 mkdir/write/chmod，写入 bytes 必须匹配已核对源哈希；每项只有一次，不能变成任意 shell、删除或覆盖权限。进度与环境操作结果在同一事务提交；初始化未封存或发生未确认结果时禁止普通文件操作、模型命令和快照导出/保存。全项确认后封存；拒绝重复初始化写入，精确封存回执可以重读。
- 初始化元数据和增长上限按固定 256 KiB 纳入工作区配额；删除预览纳入清单/状态/操作进度，项目关闭清除资源引用和进度，迟到操作不能恢复已删除的记录；有界清理将未完成初始化标为失败。没有迁移用户数据库。
- 三个内部 HTTP 路由提供 prepare/file/seal，身份和租约校验先于请求体处理。Sidecar 仅在这些资源路由扩大相应响应界限，保留凭据保护、禁止代理/重定向和未知结果不自动重试的原传输约束。
- SDK `RuntimeResourceFile` 通过原生 BaseEntry 注册和序列化保存源引用，读取校验后的二进制资源再复用 SDK `File.apply`、Dir 与 Manifest 装载。`RuntimeSandboxClient(sources=...)` 在创建时获取 Runtime 清单；装载完成关闭初始化授权。后续修改走原审批，保存重建只恢复快照，不重新装载原始文件。二进制附件不强行塞入 UTF-8 JSON 字段，也没有另写补丁或 Agent 执行循环。
- 专项 95 项通过（13.61 秒，`.tmp/goal-g434-manifest-final.xml`）：SDK 具体会话、真实临时多文件 Skill/90 KiB 二进制附件、历史版本文件、审批后修改、保存/重建、丢失/取消/封存回执失败及路径/哈希错误；另有三身份 HTTP 协议测试及 SDK+真实回环 HTTP+临时文件完整往返。HTTP/持久存储/引擎为协议夹具，不是 Go Store/OCI 联合。Windows 无法提供 POSIX mode 位，chmod 回执明确模拟，其余文件 bytes/归档实际读写。
- Go 新增 6 组 Runtime 测试与 2 组 HTTP 测试，覆盖三模式、版本冻结/已安装 Skill、拒绝冒充写权限、一次性操作、数据库重开、初始化未知结果、删除迟到回执及 v60 备份迁移；只通过 build/vet，未运行 Go 测试程序，沿用既有 Application Control 阻塞，不换路径或策略绕过。
- 首次 SDK 受限测试在旧隔离临时目录清理遇到 WinError 5，经审查以同路径重跑。后续发现 Windows chmod 不能代表 POSIX 验证，以及 HTTP 夹具直接序列化 datetime 的错误，已在夹具中明确建模/修正。第一次完整回归 1161 通过、该 HTTP 夹具 1 失败，不作为最终通过证据；修正后专项95项通过，完整 SDK 回归1162项通过（299.80秒，`.tmp/goal-g434-sdk-all-final.xml`，退出码0）。Go 全后端 build及相关四包vet退出码0，日志 `.tmp/goal-g434-build.log`、`.tmp/goal-g434-vet.log`；diff check通过。

本批仍不是生产入口交付：尚需固定 SDK Filesystem/Shell/Skills 完整工具装配/noexec 助手、PTY 与生产清理配置、三个 Runner 的实际接线/交付确认，以及成果发布和用户操作闭环。真实 Go/OCI、网关、浏览器和最终全局 review 仍未通过；不新增产品方向，不 push、部署或操作 8860/8880、测试机、用户作品/数据库。

### G4.35 SDK 原生能力装配与 Runner 验证

- `prepare_native_workspace` 复用 SDK Filesystem/Shell 的 `configure_tools` 回调：工具在会话绑定后才生成，因此在该时刻接入原 Runtime 包装器、持久审批、审计和数据策略。仅过滤实际允许的工具，不重写 SDK 编辑器、命令解析、图片工具或 Agent 循环；保留原 Agent 的配置字段和调用者的 Session 压缩，不另加 Sandbox Compaction。
- 初始资源清单现在可在 Runner 处理 capability manifest 前预备，只保留一份执行租约，不创建环境。随后仍由 SDK Runner 创建、启动、保存和关闭会话。预备成功不重复取资源；丢失或取消回执不自动重放；创建前核对仍持有同一新租约。恢复依旧使用选定快照，不覆盖为初始文件。
- `.skills` 目录交给 SDK Skills 读取元数据并加入指令；模板清单不会覆盖已选定资源。当前 provider 的基础启动助手为空，真实 Runner 文件/图片/单次命令路径无需新增 noexec 助手实现。只允许已有精确 chmod 文件操作，不放宽宿主路径、任意辅助 shell 或执行挂载策略；真实 Linux/OCI 行为仍待授权环境验证。
- Runtime 新增只读 `view_image` 描述符，二进制模型输出上限与已有 Skill 图片通道同为16 MiB；平台审计仅存省略标记、编码长度和哈希，保留 SDK 的结构化图片输出。禁用、只读过滤和延迟发现同时验证，不借工具搜索重新开放写权限。
- 当前会话未提供 PTY。SDK 的非 PTY 路径会把 `tty=true` 落到单次执行，本适配在审批登记和执行前明确拒绝，不能把它报成交互成功。`write_stdin`、真正 PTY 生命周期继续归 D2，尚未交付。
- 新增22个参数化测试，使用实际 SandboxAgent/Runner、真实临时文件和快照；模型、Runtime 审批/引擎为隔离替身。覆盖已安装 Skill 元数据、工具过滤/延迟搜索、补丁和单次命令审批后 RunState 序列化/重建的批准与拒绝、300 KiB 以上有效 PNG 的结构化模型输出和审计脱敏、策略拒绝、预备未知结果，以及最终文本存在但关闭未确认时拒绝平台交付。此批 Runner 测试使用非流式入口，不替代后续三个生产流式/非流式入口联合验收。
- 验证：专项96项通过（17.26秒，`.tmp/goal-g435-native-final.xml`）；完整SDK1184项通过（235.15秒，`.tmp/goal-g435-sdk-all-final.xml`），退出码均0。Go全后端build及Runtime/HTTP/scriptsandbox/agenttool四包vet退出码0，日志为`.tmp/goal-g435-build.log`、`.tmp/goal-g435-vet.log`；diff check通过。Go测试程序仍受Application Control阻止，未尝试执行或绕过。
- 首次受限pytest清理旧临时目录遇到WinError 5，经审查以同一已核对的仓库路径重跑。中途两个测试断言未包含SDK包装后的UserError，已修正并确认拒绝发生在工具调用登记/副作用之前；不作为代码执行成功掩盖失败。

下一项仍为D2交互执行/清理配置与D3实际入口接线。三个入口中的暂停续跑、格式兼容重试和结果修复再次调用必须保留原工作区并确认保存/关闭，不可重新创建空目录；这是原恢复门禁，不是新产品范围。成果发布/安装/UI、同版完整回归、真实环境验收及最终全局review仍未完成。Goal保持active，不push、不部署、不操作8860/8880、测试机、用户作品/数据库或系统安全策略。

### G4.36 服务器生命周期接线与工作区续租

- 核对到已有原生引擎注入和Sweep此前未接服务器启动。现已增加`CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED`，默认false；true仅复用已配置的具体OCI适配器，不创建另一套引擎、不隐式启用管理员策略、不拉镜像或回退宿主机执行。服务器启动日志只称configured，不把静态配置称为实际引擎验收。
- `RunCleanup`仅允许无AgentActivity的宿主服务身份，启动后执行有界批次，每批最多8个、整批两分钟期限，完成后再等一分钟，避免重叠清理。只使用原Sweep/精确绑定句柄/未知结果保留逻辑；日志为有界计数，不包含引擎错误原文或凭据。服务监听和Agent turn manager成功启动后才启动worker；关停时先取消，最多等待20秒再关闭Store，超时明确未确认，由下次获授权启动处理持久pending记录。没有启动本机worker或修改环境变量。
- 发现现有工作区租约为60秒，但原生适配尚无持续续租；长模型等待会使后续操作过期。本批增加专用renew方法/HTTP路由，校验原用户权限、执行epoch/dispatch、holder/generation和未过期状态，只延长当前租约。允许pausing阶段完成已有工作区保存，不允许获得新租约、接管、复活过期持有者或创建环境。
- Sidecar新增仅运行期存在的`NativeWorkspaceLeaseGuard`，15秒间隔、10秒请求期限；没有工作区时不预占资源，失联/超时不重试，取消拥有Runner的任务并拒绝成功交付。退出时回收续租任务且只清除自身发起的取消计数；用户取消与失联竞争时保留用户取消。续租回执不能变更session/generation，也不能把并发保存后更高的本地snapshot_version降回旧值。
- 新增Go服务器配置/清理等待、Runtime清理/取消/未知结果、三模式续租/过期/冒用/暂停及HTTP鉴权前置测试。仅编译和vet，未运行Go测试程序，不绕过Application Control。Python新增18项，用随机回环HTTP覆盖三身份协议，并用真实SDK Runner验证模型等待期间续租、失联/超时停止及取消竞争；审批/存储/环境仍是隔离替身，不是Go/OCI联合。
- 专项98项通过（13.56秒，`.tmp/goal-g436-lease-final.xml`）；本版完整SDK1202项通过（177.04秒，`.tmp/goal-g436-sdk-all-final.xml`），退出码0。Go全后端build与cmd/server、runtime、httpapi、scriptsandbox、agenttool五包vet退出码0，日志`.tmp/goal-g436-build.log`、`.tmp/goal-g436-vet.log`。diff check通过，未把前版全量当本版通过。

生产入口接线仍未完成，续租guard不能仅存在于工具模块就算已使用。接线时须处理当前主对话传输尚未带dispatch_generation、恢复清单必须取Runtime权威版本、成功提交前完成原生保存/关闭、流式取消等待SDK资源清理，以及修复/续跑不重复创建环境。这些是原D3恢复与交付门禁的具体接点，不新增功能方向。PTY、二进制成果发布/安装/UI、同版完整回归及最终全局review仍保留未完成；Goal active，未部署、未改用户服务/作品/数据库/凭据或系统策略。

## 历史推进顺序

以下保留各批当时的下一步，已完成项以 G4.11b/G4.12 和上方“紧接工作”为准，不作为当前待开发清单。

- 后台真正暂停/继续已完成本批接线与分层隔离验证，继续运行中追加/排队消息的原生 SDK 接线及 UI 操作语义，区分当前轮追加、下一轮、停止和已发出副作用；不得把暂停/恢复或 cancel/retry 当作追加要求的替代。
  G4.3 已完成本人未开始消息的文本编辑、取消入口和隔离分层验证；G4.4 接通后台文本追加的持久记录、原生暂停恢复、审批边界与收件 UI。下一步必须继续主对话及状态化的当前执行追加，不把后台局部能力或普通队列管理当作三模式替代；同时仍需核对消息列表截断、乐观消息身份匹配以及取消 HTTP 等待与真正停止执行的区分。
  G4.5 已完成 Sidecar 暂停基础及自定义终止行为的原生审批处理后追加验证。G4.6 已接 Go `agent_turns` 状态 CHECK 迁移、无审批 checkpoint/哈希验证、manager 暂停通知、shell paused 协议、用户权限/显式继续与 UI，并通过真实 Go+Python SDK 写文件/暂停/重建/继续的确定性联跑，证据见上。紧接主对话不可变输入、收件回执和原生追加，同时扩充审批/失败的跨进程矩阵；不能将暂停控制当作当前执行追加完成。已有审批中的人工暂停不能因批准而自动恢复；先处理旧批准工具的原生路径不能冒充撤销副作用。Go `AcceptAgentTurn` 已持久保存 accepted 轮，`ClaimRunnableAgentTurns` 已按会话串行领取，普通下一轮排队不必再造；后台已接的追加不改写主对话队列、会话或冻结输入。

- G4.7b 已接主对话 manager/shell/Sidecar 输入传输、原生消费、旧审批处理、模型收件和用户 API/UI，详见上方新增记录，取代 G4.7a 当时的“尚未开放”状态。继续状态化当前执行追加、附件/二进制、撤回修订、失败后安全继续及跨进程完整审批/失败矩阵；不能把主对话确定性联跑和前端模拟 API 合并算成真实模型三模式用户验收。

- G4.8 已接状态化 SDK 生成阶段和源分析批次的原生暂停、无审批 checkpoint、原 attempt 恢复与旧审批/取消/删除边界；不需要再次实现这一层。下一步先延伸输出修复与后端拒收修复阶段的持久恢复，再连接状态化追加输入；区分 Run 范围、并行任务和已完成批次，不能改写冻结 ContextPack 的哈希或重生成旧批次来规避约束。视频专用执行继续遵循已有业务边界，不假称支持文本 SDK 中间状态。
  G4.9 已完成本地解析/合同修复的原生恢复与分批/用量，并以真实 Go+SDK 关闭重建验证，勿重复实现。**下一动作改为补真正 CommitExecutionResult 拒收通道**：先以实际 Go 正式提交校验失败复现，保留不可变被拒 payload/hash/usage/error，设计有界修复的幂等授权领取和原 attempt 继续；覆盖 result_received、丢失回执、重建、租约、取消/暂停/删除、已发生工具副作用、第二次拒收终止和公开状态。不能仅把 catch 从 submit 移到 commit，也不能允许随意覆盖已接收结果或回放原生成；需让新 Worker 在原结果上进入修复，之后再连接状态化追加。
  G4.10 已完成正式拒收通道的边界核查、迁移/Store 删除、HTTP、真实 Go+SDK 确定性联跑、公开状态/前端分层交互及扩大回归，见收口记录；**下一动作是状态化追加输入**。旧 G4.9 段落记载当时缺口，不是要求再次重做修复。当前仍不能勾选 GAP-11/15，完整外部模型三模式同场验收另行保留。
  G4.11a 已完成追加的 SDK 原生输入/checkpoint 层和上述确定性专项，**下一动作是 Go 不可变输入、当前领取绑定/模型回执、自动暂停与人工/审批暂停边界，再接用户 API/UI**。不要再次从 RunState.add_input 或输出修复开始；也不能把当前内部输入层算作状态化追加已可供用户操作。

- 复核固定 SDK SandboxAgent/Filesystem/Shell/Skills 与现有 Go 安装、资产、审批和沙箱边界的连接方式；避免再造每 Skill 的执行器。
- 普通 Artifact 下载、Hosted 输出/引用/材料入口和结构化输入已补，继续 MCP 产出持久化、二进制入口，以及作者工具在真实模型、含依赖 Skill、后台/状态化模式中的完整闭环，不把本批局部通过当作全部场景通过。
- GAP-09 的执行身份、冻结 Run 目录、Worker 续租与原生审批状态已接到实际 SDKTaskWorker；G3.7 已完成无新依赖作者任务的 Go + Sidecar 跨进程批准/拒绝联跑。继续含依赖 Skill、真实模型和三模式联合验收，逐项核对序列化契约；已冻结旧 ContextPack 的跨版本恢复与输出合同兼容仍需明确验证，不能改写其输入哈希规避问题。
- 状态化批量源分析已保留完成批次结果/用量和当前批次原生 SDK 状态。G3.6 已补手动失败重试门禁与恢复队列分页；继续含副作用失败的结果核对/安全再执行流程、终态 checkpoint 保留策略，以及首次领取队列 LIMIT 100 在能力过滤之前导致的饥饿风险。
- 状态化工具准备与 AgentContext/RunState 已接通；G3.8 已补当前执行内作者安装的脚本/已配置 MCP 依赖激活及隔离测试。仍需当前执行发现/路由其他 Skill、新 handoff、真实脚本/Hosted 工具及新依赖跨进程联合验收。保留视频已有独立执行边界，不借此重写其业务 Prompt。
- G3.7 已补公开执行归属、前端通用审批卡和历史分组；Go/SDK 联跑验证公开审批及 projection，桌面/手机另做 API fixture 交互。仍需实际模型、三模式与前端同场联合验收，以及带依赖作者安装、工具路由和恢复边缘；不能将分层通过合并宣称为完整 E2E。
- 按审计 GAP-09 至 GAP-17 推进工具模式一致性、控制命令、MCP 凭据、Hosted 材料、输出、追加要求、协作和规则。

## 外部验收条件

- 原生压缩网关兼容性仍是既有阻塞，未授权变更网关。
- 真实 OCI 环境此前不可用；未获得安装授权前不擅自安装或修改宿主环境。
- 所有被外部条件阻塞的能力保持未通过，不影响推进其他可实施项，也不能因此标记整个 Goal 完成。
