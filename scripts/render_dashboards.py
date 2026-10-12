"""Editable source for the Stash and CLIP worker dashboard."""
import argparse
import json
from pathlib import Path

ROOT=Path(__file__).resolve().parent.parent/"monitoring"
DS={"type":"prometheus","uid":"prometheus"}
STATE_MAPPINGS=[{"type":"value","options":{"0":{"text":"Offline","color":"#8e8e8e"},"1":{"text":"Готов","color":"green"},"2":{"text":"Занят","color":"blue"}}}]

def panel(
    title, kind, x, y, w, h, queries=(), unit="short", description="", mappings=None
):
    targets = [
        {
            "refId": chr(65 + i),
            "expr": expr,
            "legendFormat": legend,
            "datasource": DS,
            "range": True,
            "instant": False,
        }
        for i, (expr, legend) in enumerate(queries)
    ]
    return {
        "title": title,
        "type": kind,
        "datasource": DS,
        "gridPos": {"x": x, "y": y, "w": w, "h": h},
        "description": description,
        "targets": targets,
        "fieldConfig": {
            "defaults": {
                "unit": unit,
                "noValue": "—",
                "color": {"mode": "palette-classic"},
                "mappings": mappings or [],
                "thresholds": {
                    "mode": "absolute",
                    "steps": [{"color": "green", "value": None}],
                },
                "custom": {"lineWidth": 2, "fillOpacity": 10, "spanNulls": False},
            },
            "overrides": [],
        },
        "options": {
            "legend": {"displayMode": "list", "placement": "bottom"},
            "tooltip": {"mode": "multi", "sort": "desc"},
        },
    }

def stat(title, expr, x, unit="short", description="", health=False):
    result = panel(title, "stat", x, 0, 4, 4, [(expr, "")], unit, description)
    result["targets"][0].update(range=False, instant=True)
    defaults = result["fieldConfig"]["defaults"]
    defaults["color"] = {"mode": "thresholds"}
    if "ошиб" in title.lower() or "lease за период" in title:
        defaults["thresholds"]["steps"].extend(
            [{"color": "orange", "value": 1}, {"color": "red", "value": 5}]
        )
    elif "доступны" in title.lower():
        defaults["thresholds"]["steps"] = [
            {"color": "#8e8e8e", "value": None},
            {"color": "green", "value": 1},
        ]
    result["options"] = {
        "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
        "orientation": "auto",
        "textMode": "auto",
        "colorMode": "value",
        "graphMode": "none",
    }
    if health:
        result["fieldConfig"]["defaults"]["mappings"] = [
            {
                "type": "value",
                "options": {
                    "0": {"text": "Недоступен", "color": "red"},
                    "1": {"text": "Доступен", "color": "green"},
                },
            }
        ]
    return result

def timeline(expr, x, y, w=24):
    result = panel(
        "Доступность и занятость воркеров",
        "state-timeline",
        x,
        y,
        w,
        8,
        [(expr, "{{service}} / {{worker_id}}")],
        mappings=STATE_MAPPINGS,
        description="Offline — нет ready-отчёта в пределах TTL (90 с по умолчанию). Готов — модели доступны, Занят — есть действующий lease. Пробел означает отсутствие наблюдений, а не offline. У Stash ready подтверждает наличие моделей в Ollama; у CLIP — загрузку модели. Это не гарантия успешного инференса. Простой Mac/WSL ожидаем.",
    )
    result["options"] = {
        "mergeValues": True,
        "showValue": "auto",
        "alignValue": "center",
        "rowHeight": 0.8,
        "legend": {"displayMode": "list", "placement": "bottom"},
        "tooltip": {"mode": "single"},
    }
    result["fieldConfig"]["defaults"]["custom"] = {
        "lineWidth": 0,
        "fillOpacity": 85,
        "spanNulls": False,
    }
    return result

def text(title, content, y):
    result = panel(title, "text", 0, y, 24, 4)
    result["options"] = {"mode": "markdown", "content": content}
    return result


def dashboard():
    service='{service=~"$service"}'
    worker='{service=~"$service",worker_id=~"$worker"}'
    preferred='{service=~"$service",worker_id=~"$worker",worker_role="preferred"}'
    def query(name):
        return name+worker
    panels=[
        stat("Координаторы доступны", 'sum(up'+service+')', 0, description="Ожидается 2 при выборе обоих сервисов. Ошибка сбора означает неизвестное состояние очереди."),
        stat("Mac / WSL доступны",'sum(compute_worker_online'+preferred+')',4),
        stat("Ожидают",'sum(compute_jobs{service=~"$service",status="queued"}) + sum(compute_expired_jobs'+service+')',8,description="Включая задания с истёкшим lease, которые можно забрать повторно."),
        stat("Активные задания",'sum(compute_worker_active_jobs'+worker+')',12),
        stat("Готово за период",'sum(increase('+query('compute_jobs_completed_total')+'[$__range]))',16,description="Счётчики сохраняются в БД с момента установки мониторинга. increase экстраполирует края диапазона и может дать дробное значение."),
        stat("Терминальные ошибки",'sum(increase('+query('compute_jobs_failed_total')+'[$__range]))',20,description="Задания, которые исчерпали повторы или завершились постоянной ошибкой."),
        timeline(query('compute_worker_state'),0,4,12),
        panel("Очереди и обработка","timeseries",12,4,12,8,[('compute_jobs{service=~"$service",status=~"queued|running"}',"{{service}} · {{status}}"),('compute_expired_jobs'+service,"{{service}} · истёк lease")]),
        panel("Текущее состояние воркеров","table",0,12,12,8,[(query('compute_worker_state'),"")],mappings=STATE_MAPPINGS),
        panel("Доступность за выбранный период","bargauge",12,12,12,8,[('avg_over_time('+query('compute_worker_online')+'[$__range])*100',"{{service}} / {{worker_id}}")],unit="percent",description="Доля готовности среди успешных наблюдений. Пробелы при недоступном координаторе и время до установки мониторинга не считаются offline."),
        panel("Возраст старейшего ожидающего задания","timeseries",0,20,8,7,[('compute_queue_oldest_wait_seconds'+service,"{{service}}")],unit="s"),
        panel("Возраст текущей попытки","timeseries",8,20,8,7,[(query('compute_worker_attempt_age_seconds'),"{{service}} / {{worker_id}}")],unit="s"),
        panel("Серверному fallback разрешено брать задания","state-timeline",16,20,8,7,[('compute_fallback_allowed'+service,"{{service}}")],mappings=[{"type":"value","options":{"0":{"text":"Работают Mac / WSL","color":"blue"},"1":{"text":"Разрешён fallback","color":"orange"}}}],description="Fallback разрешён, когда нет готовых приоритетных воркеров с совместимыми моделями. Это не означает, что сервер сейчас занят. TTL отчёта — 90 секунд."),
        panel("Успешных заданий в минуту","timeseries",0,27,12,8,[('rate('+query('compute_jobs_completed_total')+'[$__rate_interval])*60',"{{service}} / {{worker_id}}")]),
        panel("Повторы и ошибки попыток / мин","timeseries",12,27,12,8,[('sum by(service)(rate('+query('compute_retry_attempts_total')+'[$__rate_interval]))*60',"{{service}} · повторы"),('sum by(service)(rate('+query('compute_attempt_failures_total')+'[$__rate_interval]))*60',"{{service}} · ошибки"),('sum by(service)(rate('+query('compute_lease_expirations_total')+'[$__rate_interval]))*60',"{{service}} · истёкшие lease")]),
        panel("Время успешной попытки · P50 / P95 за 24 часа","timeseries",0,35,12,8,[('compute_job_processing_seconds{service=~"$service",worker_id=~"$worker"}',"{{service}} / {{worker_id}} · {{quantile}}")],unit="s",description="Точные квантили завершённых за последние 24 часа заданий, начиная с установки мониторинга. Включает загрузку модели и сетевое время попытки. Зависит от типа и размера входа; это не бенчмарк устройств."),
        panel("От постановки до результата · P50 / P95 за 24 часа","timeseries",12,35,12,8,[('compute_job_turnaround_seconds{service=~"$service",worker_id=~"$worker"}',"{{service}} / {{worker_id}} · {{quantile}}")],unit="s",description="Включает ожидание воркера, паузы между повторами и успешную попытку. Квантили из последних 24 часов; пусто до первого нового результата."),
        panel("Возраст последнего отчёта воркера","timeseries",0,43,12,7,[(query('compute_worker_last_report_age_seconds'),"{{service}} / {{worker_id}}")],unit="s",description="После 90 секунд воркер перестаёт быть доступным. Для ещё ни разу не подключавшегося воркера метрики возраста нет."),
        panel("Готовые инстансы каждого worker_id","timeseries",12,43,12,7,[(query('compute_worker_instances'),"{{service}} / {{worker_id}}")],description="Больше одного может означать перекрытие при обновлении или дублирующий запуск."),
        panel("Сбор метрик координаторов","state-timeline",0,50,12,6,[('up'+service,"{{service}}")],mappings=[{"type":"value","options":{"0":{"text":"Нет метрик","color":"red"},"1":{"text":"Метрики доступны","color":"green"}}}]),
        panel("Сохранённые результаты и конечные ошибки","timeseries",12,50,12,6,[('compute_jobs{service=~"$service",status=~"done|failed"}',"{{service}} · {{status}}")],description="Текущее число записей в очередях. Терминальные записи хранятся 30 дней после последнего обращения; это gauge, а не накопительный счётчик."),
        text("Как читать дашборд", "**Stash**: vision-описания через qwen2.5vl:3b и текстовые эмбеддинги bge-m3:567m. **CLIP**: ViT-B/32 / laion2b_s34b_b79k, CPU и GPU используют одно пространство векторов.\n\n**Offline у Mac/WSL ожидаем**. Ready-отчёты приходят каждые 20 с, TTL — 90 с. Задания сохраняются, fallback берёт их при отсутствии готовых приоритетных воркеров. Занятость определяется активным lease. Пробелы в истории означают отсутствие наблюдений.\n\nСчётчики переживают перезапуски и очистку очередей; история начинается с установки мониторинга. Время обработки доступно для новых завершённых заданий. RAM/VRAM удалённых машин здесь пока не собираются. Метрики не содержат пользовательского текста, медиа, job_id и токенов.",56),
    ]
    table=panels[8]
    table['targets'][0].update(format='table',instant=True,range=False)
    table['options']={"showHeader":True,"cellHeight":"sm"}
    table['transformations']=[{"id":"organize","options":{"excludeByName":{"Time":True,"instance":True,"job":True},"renameByName":{"service":"Сервис","worker_id":"Воркер","worker_role":"Роль","Value":"Состояние"}}}]
    uptime=panels[9]
    uptime['targets'][0].update(instant=True,range=False)
    uptime['options']={"orientation":"horizontal","displayMode":"basic","showUnfilled":True,"reduceOptions":{"calcs":["lastNotNull"],"fields":"","values":False}}
    uptime['fieldConfig']['defaults'].update(min=0,max=100)
    for item in panels:
        if item['type']=='state-timeline':
            item['options']={"mergeValues":True,"showValue":"auto","alignValue":"center","rowHeight":0.8,"legend":{"displayMode":"list","placement":"bottom"},"tooltip":{"mode":"single"}}
            item['fieldConfig']['defaults']['custom']={"lineWidth":0,"fillOpacity":85,"spanNulls":False}
    for i,item in enumerate(panels,1):item['id']=i
    variables=[]
    for name,label,query in [('service','Сервис','label_values(compute_worker_online, service)'),('worker','Воркер','label_values(compute_worker_online{service=~"$service"}, worker_id)')]:
        variables.append({"name":name,"label":label,"type":"query","datasource":DS,"definition":query,"query":{"query":query,"refId":name+"-variable"},"refresh":1,"multi":True,"includeAll":True,"allValue":".*","current":{"text":"All","value":"$__all"},"sort":1})
    return {"uid":"compute-workers","title":"Stash и CLIP · воркеры и очереди","id":None,"version":1,"schemaVersion":41,"tags":["stash","clip","workers","queues"],"timezone":"browser","editable":False,"refresh":"15s","time":{"from":"now-6h","to":"now"},"annotations":{"list":[]},"templating":{"list":variables},"panels":panels,"links":[{"title":"Расшифровки · воркеры","url":"/d/transcriber-workers","type":"link","keepTime":True}]}

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--check',action='store_true')
    parser.add_argument('--rules',action='store_true')
    args=parser.parse_args()
    document=dashboard()
    if args.rules:
        rules=[]
        for item in document['panels']:
            for target in item['targets']:
                expr=target['expr'].replace('$__rate_interval','1m').replace('$__range','6h').replace('$service','stash|clip-embedder').replace('$worker','.*')
                rules.append({'record':'compute_dashboard_expression_'+str(len(rules)),'expr':expr})
        print(json.dumps({'groups':[{'name':'dashboard-syntax','rules':rules}]}));return
    target=ROOT/'grafana/compute-workers.json'
    content=json.dumps(document,ensure_ascii=False,indent=2)+'\n'
    if args.check:
        if not target.exists() or target.read_text()!=content:raise SystemExit('Run make dashboards')
        assert len({p['id'] for p in document['panels']})==len(document['panels'])
        assert all(p['gridPos']['x']+p['gridPos']['w']<=24 for p in document['panels'])
    else:
        target.parent.mkdir(parents=True,exist_ok=True);target.write_text(content)

if __name__=='__main__':main()
