"""Exercise the actual CLI, gh subprocess adapter, and child supervision."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

BINARY=Path(os.environ.get('GHENV_TEST_BINARY',str(Path(os.environ.get('CARGO_TARGET_DIR','target'))/'debug'/('ghenv.exe' if os.name=='nt' else 'ghenv')))).resolve()
PROFILE=dict(version=1,repository='owner/repo',default_mode='preview',secrets='TOKEN',variables='URL',prefixes=dict(TOKEN='synthetic'),aliases=dict(PUBLIC_URL='URL'),environment=dict(BUN_OPTIONS='--no-env-file'))


class CLI(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)
        self.env=dict(os.environ,GHENV_TEST_STATE=str(self.root),GHENV_TEST_BINARY=str(BINARY),GH_TOKEN='parent-private-sentinel',UNRELATED_API_KEY='parent-private-sentinel',EXPO_NO_DOTENV='0')
        if os.name!='nt':
            shim=self.root/'gh'
            shim.write_text('#!'+sys.executable+'\n'+(Path(__file__).parent/'fake_gh.py').read_text())
            shim.chmod(0o700)
            self.env['PATH']=str(self.root)+os.pathsep+self.env['PATH']
        (self.root/'ghenv.json').write_text(json.dumps(PROFILE))
    def cli(self,*args,mode='success',timeout=15):
        return subprocess.run([str(BINARY),*args],cwd=self.root,env=self.env|dict(GHENV_TEST_MODE=mode),capture_output=True,text=True,encoding='utf-8',timeout=timeout)
    def run_request(self,*flags,mode='success',script='raise SystemExit(0)'):
        return self.cli('run','--repo','owner/repo','--secrets','TOKEN','--vars','URL',*flags,'--',sys.executable,'-c',script,mode=mode)
    def test_version_help_and_invalid_options(self):
        self.assertIn('ghenv ',self.cli('--version').stdout)
        self.assertIn('source:',self.cli('--version').stdout)
        self.assertIn('ghenv run',self.cli('--help').stdout)
        for args in [('unknown',),('local',),('run','--repo','owner/repo'),('run','--repo','../repo','--secrets','TOKEN','--','true'),('run','--repo','owner/repo','--env','Production','--secrets','TOKEN','--','true'),('run','--repo','owner/repo','--','true')]:
            with self.subTest(args=args):
                result=self.cli(*args)
                self.assertNotEqual(result.returncode,0)
                self.assertNotIn('parent-private-sentinel',result.stdout+result.stderr)
        self.assertFalse((self.root/'calls.jsonl').exists())
    @unittest.skipIf(os.name=='nt','process supervision is supported on Linux and macOS')
    def test_complete_encrypted_handoff_preserves_environment_and_child_exit(self):
        script='import os; assert os.environ["TOKEN"]=="synthetic line\\n\'quoted\' $()=😀"; assert os.environ["URL"]=="http://localhost:3000"; assert "GH_TOKEN" not in os.environ; assert "UNRELATED_API_KEY" not in os.environ; assert os.environ["EXPO_NO_DOTENV"]=="1"; raise SystemExit(23)'
        result=self.run_request(script=script)
        self.assertEqual(result.returncode,23,result.stderr)
        self.assertTrue((self.root/'deleted').exists())
        self.assertNotIn('synthetic line',result.stdout+result.stderr)
        self.assertNotIn('parent-private-sentinel',result.stdout+result.stderr)
    @unittest.skipIf(os.name=='nt','process supervision is supported on Linux and macOS')
    def test_inheritance_requires_explicit_opt_out(self):
        result=self.run_request('--clean-env=false',script='import os; assert os.environ["GH_TOKEN"]=="parent-private-sentinel"; assert os.environ["EXPO_NO_DOTENV"]=="0"')
        self.assertEqual(result.returncode,0,result.stderr)
    @unittest.skipIf(os.name=='nt','synthetic gh executable requires POSIX shebang support')
    def test_failed_handoffs_do_not_launch_and_delete_found_artifacts(self):
        for mode in ['failed-run','missing-artifact','expired','download-failed','extra-name','missing-name','wrong-request','tampered','invalid-zip','duplicate','traversal']:
            with self.subTest(mode=mode):
                (self.root/'deleted').unlink(missing_ok=True)
                (self.root/'child-started').unlink(missing_ok=True)
                result=self.run_request(mode=mode,script='from pathlib import Path;Path("child-started").touch()')
                self.assertNotEqual(result.returncode,0)
                self.assertFalse((self.root/'child-started').exists())
                self.assertEqual((self.root/'deleted').exists(),mode not in ['failed-run','missing-artifact','expired'])
                self.assertNotIn('synthetic line',result.stdout+result.stderr)
    @unittest.skipIf(os.name=='nt','synthetic gh executable requires POSIX shebang support')
    def test_doctor_and_api_errors_are_actionable_and_redacted(self):
        self.assertIn('GitHub authentication: ready',self.cli('doctor','--repo','owner/repo').stdout)
        for mode,code in [('auth-required','github_auth_required'),('disabled','workflow_disabled'),('error-401','github_auth_required'),('error-403','github_access_denied'),('error-429','github_access_denied'),('error-404','github_not_found'),('error-422','github_invalid_dispatch'),('error-500','github_request_failed')]:
            with self.subTest(mode=mode):
                result=self.cli('doctor','--repo','owner/repo',mode=mode)
                self.assertEqual(result.returncode,1)
                self.assertIn(code,result.stderr)
                self.assertNotIn('synthetic-private-sentinel',result.stdout+result.stderr)
    @unittest.skipIf(os.name=='nt','synthetic gh executable requires POSIX shebang support')
    def test_oversized_subprocess_output_is_bounded(self):
        result=self.run_request(mode='oversized')
        self.assertEqual(result.returncode,1)
        self.assertLess(len(result.stdout+result.stderr),1000)
    @unittest.skipIf(os.name=='nt','synthetic gh executable requires POSIX shebang support')
    def test_cancellation_of_download_still_uses_a_fresh_cleanup_deadline(self):
        started=time.monotonic()
        result=self.run_request('--timeout','2s',mode='download-hang')
        self.assertEqual(result.returncode,1)
        self.assertIn('deadline exceeded',result.stderr)
        self.assertLess(time.monotonic()-started,5)
        self.assertTrue((self.root/'deleted').exists())
    @unittest.skipIf(os.name=='nt','process supervision is supported on Linux and macOS')
    def test_dev_preview_prefixes_aliases_and_local_mode(self):
        result=self.cli('dev','--','--not-a-command')
        self.assertNotEqual(result.returncode,0)
        script='import os; assert os.environ["TOKEN"].startswith("synthetic");assert os.environ["PUBLIC_URL"]==os.environ["URL"];assert os.environ["BUN_OPTIONS"]=="--no-env-file";assert "GH_TOKEN" not in os.environ'
        result=self.cli('dev','--',sys.executable,'-c',script)
        self.assertEqual(result.returncode,0,result.stderr)
        (self.root/'calls.jsonl').unlink()
        result=self.cli('dev','--mode','local','--',sys.executable,'-c','import os;assert "TOKEN" not in os.environ;assert "URL" not in os.environ;assert "GH_TOKEN" not in os.environ;assert os.environ["BUN_OPTIONS"]=="--no-env-file"')
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertFalse((self.root/'calls.jsonl').exists())
    def test_invalid_profiles_fail_without_requesting_credentials(self):
        for text in ['{}','null',json.dumps(PROFILE).replace('"version": 1','"version": 1, "version": 1'),json.dumps(PROFILE).replace('"secrets"','"Secrets"'),json.dumps(PROFILE).replace('"secrets"','"ſecrets"')]:
            (self.root/'ghenv.json').write_text(text)
            result=self.cli('dev','--',sys.executable,'-c','raise SystemExit(0)')
            self.assertEqual(result.returncode,1)
        self.assertFalse((self.root/'calls.jsonl').exists())
    @unittest.skipIf(os.name=='nt','process supervision is supported on Linux and macOS')
    def test_local_is_offline_and_preserves_arguments_and_exit_code(self):
        script='import os,sys;assert "GH_TOKEN" not in os.environ;assert "UNRELATED_API_KEY" not in os.environ;assert os.environ["EXPO_NO_DOTENV"]=="1";assert sys.argv[1:]==["hello world","--flag","$()=😀"];raise SystemExit(23)'
        result=self.cli('local','--',sys.executable,'-c',script,'hello world','--flag','$()=😀')
        self.assertEqual(result.returncode,23,result.stderr)
        self.assertFalse((self.root/'calls.jsonl').exists())
    @unittest.skipIf(os.name=='nt','POSIX byte arguments and environment')
    def test_non_utf8_child_arguments_and_unrelated_environment_do_not_panic(self):
        self.env['UNRELATED_BYTES']=b'\xff'
        result=self.cli('local','--',sys.executable,'-c','import os,sys;assert os.fsencode(sys.argv[1])==b"\\xff";assert b"UNRELATED_BYTES" not in os.environb',b'\xff')
        self.assertEqual(result.returncode,0,result.stderr)
        result=self.run_request('--clean-env=false',script='import os;assert os.environb[b"UNRELATED_BYTES"]==b"\\xff"')
        self.assertEqual(result.returncode,0,result.stderr)


@unittest.skipIf(os.name=='nt','POSIX terminal and process group contract')
class Supervision(CLI):
    # Inherit fixture helpers only; the loader runs CLI cases once below.
    def assert_descendant_stopped(self,pid):
        deadline=time.monotonic()+2
        while True:
            status=subprocess.run(['ps','-o','stat=','-p',str(pid)],capture_output=True,text=True).stdout.strip()
            if not status or status.startswith('Z'):return
            self.assertLess(time.monotonic(),deadline,status)
            time.sleep(.02)
    def test_foreground_terminal_input_and_restoration(self):
        import pty
        import select
        pid,fd=pty.fork()
        if pid==0:
            script='line=input();assert line=="synthetic-input";raise SystemExit(23)'
            result=subprocess.run([str(BINARY),'local','--',sys.executable,'-c',script],env=self.env,cwd=self.root)
            os._exit(result.returncode if os.tcgetpgrp(0)==os.getpgrp() else 99)
        done=False
        try:
            os.write(fd,b'synthetic-input\n')
            deadline=time.monotonic()+8
            while time.monotonic()<deadline:
                ended,status=os.waitpid(pid,os.WNOHANG)
                if ended:
                    done=True
                    self.assertEqual(os.waitstatus_to_exitcode(status),23)
                    break
                ready,_,_=select.select([fd],[],[],.05)
                if ready:
                    try:os.read(fd,4096)
                    except OSError:pass
            self.assertTrue(done,'foreground terminal input hung')
        finally:
            os.close(fd)
            if not done:os.kill(pid,signal.SIGKILL);os.waitpid(pid,0)
    def test_second_signal_accelerates_cleanup_of_an_ignoring_descendant(self):
        child_script='import os,signal,sys,time;signal.signal(signal.SIGTERM,signal.SIG_IGN);signal.signal(signal.SIGINT,signal.SIG_IGN);open(sys.argv[1],"w").write(str(os.getpid()));time.sleep(30)'
        wrapper='import subprocess,sys,time;subprocess.Popen([sys.executable,"-c",sys.argv[1],sys.argv[2]]);time.sleep(30)'
        marker=self.root/'descendant.pid'
        process=subprocess.Popen([str(BINARY),'local','--',sys.executable,'-c',wrapper,child_script,str(marker)],cwd=self.root,env=self.env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        descendant=None
        try:
            deadline=time.monotonic()+5
            while not marker.exists():
                self.assertIsNone(process.poll())
                self.assertLess(time.monotonic(),deadline)
                time.sleep(.02)
            descendant=int(marker.read_text())
            process.send_signal(signal.SIGTERM)
            time.sleep(.1)
            started=time.monotonic()
            process.send_signal(signal.SIGTERM)
            process.wait(timeout=3)
            self.assertLess(time.monotonic()-started,2)
            self.assert_descendant_stopped(descendant)
        finally:
            if process.poll() is None:process.kill();process.wait(timeout=3)
            if descendant:
                try:os.kill(descendant,signal.SIGKILL)
                except ProcessLookupError:pass
    def test_grace_deadline_cleans_up_after_a_signal_or_leader_exit(self):
        child_script='import os,signal,sys,time;signal.signal(signal.SIGTERM,signal.SIG_IGN);signal.signal(signal.SIGINT,signal.SIG_IGN);open(sys.argv[1],"w").write(str(os.getpid()));time.sleep(30)'
        wrapper='import pathlib,subprocess,sys,time;subprocess.Popen([sys.executable,"-c",sys.argv[1],sys.argv[2]]);\nwhile not pathlib.Path(sys.argv[2]).exists():time.sleep(.01)\nif sys.argv[3]=="leader-exits":raise SystemExit(0)\ntime.sleep(30)'
        for mode in ['signal','leader-exits']:
            with self.subTest(mode=mode):
                marker=self.root/(mode+'.pid')
                process=subprocess.Popen([str(BINARY),'local','--',sys.executable,'-c',wrapper,child_script,str(marker),mode],cwd=self.root,env=self.env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
                descendant=None
                try:
                    deadline=time.monotonic()+5
                    while not marker.exists():
                        self.assertIsNone(process.poll())
                        self.assertLess(time.monotonic(),deadline)
                        time.sleep(.02)
                    descendant=int(marker.read_text())
                    started=time.monotonic()
                    if mode=='signal':process.send_signal(signal.SIGTERM)
                    process.wait(timeout=8)
                    self.assertLess(time.monotonic()-started,7)
                    self.assert_descendant_stopped(descendant)
                finally:
                    if process.poll() is None:process.kill();process.wait(timeout=3)
                    if descendant:
                        try:os.kill(descendant,signal.SIGKILL)
                        except ProcessLookupError:pass


def load_tests(loader,tests,pattern):
    suite=unittest.TestSuite(loader.loadTestsFromTestCase(CLI))
    for name in ['test_foreground_terminal_input_and_restoration','test_second_signal_accelerates_cleanup_of_an_ignoring_descendant','test_grace_deadline_cleans_up_after_a_signal_or_leader_exit']:
        suite.addTest(Supervision(name))
    return suite


if __name__=='__main__':unittest.main(verbosity=2)
