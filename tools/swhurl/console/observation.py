"""Versioned Go reports at the console boundary. Validation and display, never instance policy."""
from __future__ import annotations

import json

from swhurl.apps.state import State
from swhurl.console.actions import ActionError

STATES = {'healthy', 'updating', 'failing', 'suspended', 'unknown', 'stopped'}
AVAILABILITY = {'present', 'absent', 'unavailable', 'not_read'}
ROLES = {'calls', 'called-by', 'publishes', 'subscribes', 'subscriber', 'reaches'}
OUTCOMES = {'', 'connected', 'waiting', 'refused'}
REMOVED_REASONS = {'unit', 'release', 'unit_and_release'}


def require(value, kind) -> None:
    if type(value) is not kind:
        raise ValueError('wrong field type')


def fields(value, required: str, optional: str = '') -> None:
    require(value, dict)
    if set(required.split()) - value.keys() or value.keys() - set((required + ' ' + optional).split()):
        raise ValueError('missing or unknown fields')


def strings(value) -> None:
    require(value, list)
    for item in value:
        require(item, str)


def nullable(value, kind) -> None:
    if value is not None:
        require(value, kind)


def live(value) -> None:
    fields(value, 'unit release workload running')
    for key in ('unit', 'release', 'workload', 'running'):
        fact = value[key]
        required = {'unit': 'suspended conditions', 'release': 'suspended conditions',
                    'workload': 'readyReplicas', 'running': 'digests'}[key]
        fields(fact, 'status ' + required, 'reason')
        for name in ('status', 'reason'):
            if name in fact:
                require(fact[name], str)
        if fact['status'] not in AVAILABILITY:
            raise ValueError('unknown evidence status')
        if key in ('unit', 'release'):
            nullable(fact['suspended'], bool)
            nullable(fact['conditions'], list)
            for condition in fact['conditions'] if fact['conditions'] is not None else ():
                fields(condition, 'type status reason observedGeneration')
                for name in ('type', 'status', 'reason'):
                    require(condition[name], str)
                nullable(condition['observedGeneration'], int)
        elif key == 'workload':
            nullable(fact['readyReplicas'], int)
        elif fact['digests'] is not None:
            strings(fact['digests'])


def ref(value) -> None:
    fields(value, 'apiVersion kind', 'namespace name')
    for text in value.values():
        require(text, str)


def backup(value) -> None:
    fields(value, 'state archive', 'reason schedule lastScheduledAt lastSucceededAt maxAgeSeconds newestRun')
    for key, item in value.items():
        if key not in ('archive', 'newestRun', 'maxAgeSeconds'):
            require(item, str)
    if 'maxAgeSeconds' in value:
        require(value['maxAgeSeconds'], int)
    if 'newestRun' in value:
        fields(value['newestRun'], 'name outcome', 'createdAt completedAt')
        for text in value['newestRun'].values():
            require(text, str)
    archive = value['archive']
    fields(archive, 'state', 'reason bucket prefix newest lastModified ageSeconds maxAgeSeconds')
    for key, item in archive.items():
        require(item, int if key in ('ageSeconds', 'maxAgeSeconds') else str)


def report(value: dict) -> dict:
    fields(value, 'kind version source observedAt currentness live state verdict backup findings problems routes certificates assessed unassessed notCovered target promotion')
    if value['kind'] != 'v2-observation' or type(value['version']) is not int or value['version'] != 5:
        raise ValueError('unsupported observation version')
    fields(value['state'], 'name', 'field reason')
    for text in value['state'].values():
        require(text, str)
    if value['state']['name'] not in STATES:
        raise ValueError('unknown instance state')
    fields(value['currentness'], 'state reason appliedRevision')
    if value['currentness']['state'] not in {'applied', 'unaffected', 'pending', 'unknown'}:
        raise ValueError('unknown currentness')
    for text in value['currentness'].values():
        require(text, str)
    fields(value['promotion'], 'ready reason')
    require(value['promotion']['ready'], bool)
    require(value['promotion']['reason'], str)
    live(value['live'])
    for key in ('assessed', 'unassessed', 'notCovered'):
        strings(value[key])
    for key, required, optional in (('findings', 'field outcome', 'reason'), ('problems', 'pod container reason', 'message'),
                                    ('routes', 'name outcome hosts', 'reason'), ('certificates', 'name outcome', 'reason')):
        require(value[key], list)
        for item in value[key]:
            fields(item, required, optional)
            for name, text in item.items():
                strings(text) if name == 'hosts' else require(text, str)
    require(value['verdict'], str)
    backup(value['backup'])
    target = value['target']
    fields(target, 'namespace unit release workload pods selector container digest replicas', 'revision backup route')
    require(target['replicas'], int)
    for key in ('unit', 'release', 'workload', 'pods'):
        ref(target[key])
    if 'route' in target:
        fields(target['route'], 'ingress certificates')
        ref(target['route']['ingress'])
        require(target['route']['certificates'], list)
        for cert in target['route']['certificates']:
            ref(cert)
    if 'backup' in target:
        fields(target['backup'], 'cronJob pods selector schedule')
        ref(target['backup']['cronJob'])
        ref(target['backup']['pods'])
        require(target['backup']['selector'], str)
        require(target['backup']['schedule'], str)
    for key in ('namespace', 'selector', 'container', 'digest'):
        require(target[key], str)
    for key in ('source', 'observedAt'):
        require(value[key], str)
    return value


def unique(pairs):
    found = {}
    for key, value in pairs:
        if key in found:
            raise ValueError('duplicate JSON field')
        found[key] = value
    return found


def inventory_block(value: dict) -> None:
    """What an instance's records state, beside its report: the facts a row shows, never a second verdict."""
    fields(value, 'team kind exposure port hosts address api artifact database offers egress calls allows callers publishes subscriptions connections')
    for key in ('team', 'kind', 'exposure', 'address', 'api', 'offers'):
        require(value[key], str)
    require(value['port'], int)
    strings(value['hosts'])
    for block in ('artifact', 'database'):
        fields(value[block], 'image digest' if block == 'artifact' else 'kind path')
        for text in value[block].values():
            require(text, str)
    fields(value['egress'], 'port grants')
    require(value['egress']['port'], int)
    strings(value['egress']['grants'])
    for key in ('calls', 'allows', 'callers', 'publishes', 'subscriptions'):
        strings(value[key])
    require(value['connections'], list)
    for row in value['connections']:
        fields(row, 'role', 'app event host outcome reason')
        if row['role'] not in ROLES or row.get('outcome', '') not in OUTCOMES:
            raise ValueError('unknown connection row')
        for text in row.values():
            require(text, str)


def decode(text: str, revision: str, *, single: bool = False) -> dict:
    try:
        value = json.loads(text, object_pairs_hook=unique)
        if single:
            found = report(value)
            if found['target'].get('revision') != revision:
                raise ValueError('wrong observation revision')
            return found
        fields(value, 'kind version source observedAt revision instances removing')
        if value['kind'] != 'v2-observations' or type(value['version']) is not int or value['version'] != 2:
            raise ValueError('unsupported observations version')
        if value['revision'] != revision:
            raise ValueError('wrong observations revision')
        for key in ('source', 'observedAt'):
            require(value[key], str)
        require(value['instances'], list)
        found = {}
        for instance in value['instances']:
            fields(instance, 'application environment report inventory')
            require(instance['application'], str)
            require(instance['environment'], str)
            key = (instance['application'], instance['environment'])
            if key in found:
                raise ValueError('duplicate instance')
            inventory_block(instance['inventory'])
            found[key] = {'report': report(instance['report']), 'inventory': instance['inventory']}
            if found[key]['report']['target'].get('revision') != revision:
                raise ValueError('wrong observation revision')
        require(value['removing'], list)
        removing = []
        for gone in value['removing']:
            fields(gone, 'application environment reason')
            for text in gone.values():
                require(text, str)
            if gone['reason'] not in REMOVED_REASONS:
                raise ValueError('unknown removal reason')
            removing.append((gone['application'], gone['environment'], gone['reason']))
        return {'instances': found, 'removing': tuple(removing)}
    except (ValueError, TypeError, KeyError) as error:
        raise ActionError(f'Go status unavailable: invalid report ({type(error).__name__})') from None


def state(report: dict) -> State:
    value = report['state']
    return State(value['name'], value.get('reason', '').replace('_', ' '))


def promotion_problem(report: dict) -> str:
    decision = report['promotion']
    return '' if decision['ready'] else decision['reason'].replace('_', ' ')
