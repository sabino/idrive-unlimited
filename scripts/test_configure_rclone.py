import configparser
import os
import pathlib
import secrets
import shutil
import subprocess
import sys
import tempfile
import unittest

class PrivateConfigurationTests(unittest.TestCase):
    def setUp(self):
        self.binary = os.environ.get('IDRIVE_RCLONE_BINARY') or shutil.which('rclone')
        if not self.binary:
            self.skipTest('rclone is required')
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = pathlib.Path(self.temporary.name)
        self.secret = secrets.token_urlsafe(32)
        self.password = self.root/'password'
        self.password.write_text(self.secret+'\n')
        self.password.chmod(0o600)
        self.config = self.root/'rclone.conf'

    def invoke(self, *extra):
        return subprocess.run([sys.executable,str(pathlib.Path(__file__).with_name('configure-rclone.py')),
            '--password-file',str(self.password),'--config',str(self.config),*extra],
            capture_output=True,text=True,env=dict(os.environ,IDRIVE_RCLONE_BINARY=self.binary))

    def test_real_rclone_config_private_and_usable(self):
        p = self.invoke()
        self.assertEqual(p.returncode,0,p.stderr)
        self.assertNotIn(self.secret,p.stdout+p.stderr)
        config=configparser.ConfigParser(interpolation=None);config.read(self.config)
        obscured=config['idrive-local']['pass']
        self.assertTrue(obscured)
        self.assertNotEqual(obscured,self.secret)
        result=subprocess.run([self.binary,'--config',str(self.config),'config','redacted','idrive-local'],capture_output=True,text=True)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertIn('[idrive-local]',result.stdout)
        if os.name!='nt':self.assertEqual(self.config.stat().st_mode & 0o077,0)

    def test_other_remotes_are_preserved(self):
        self.config.write_text('[unrelated]\ntype = local\n')
        p = self.invoke()
        self.assertNotEqual(p.returncode,0)
        self.assertEqual(self.config.read_text(),'[unrelated]\ntype = local\n')
        self.assertNotIn(self.secret,p.stdout+p.stderr)

    def test_remote_credentials_are_not_sent_to_nonloopback_url(self):
        p=self.invoke('--url','https://outside.invalid/')
        self.assertNotEqual(p.returncode,0)
        self.assertFalse(self.config.exists())
        self.assertNotIn(self.secret,p.stdout+p.stderr)

if __name__=='__main__':unittest.main()
